#!/usr/bin/env python3
"""Small CI adapters; release policy and manifest generation live in releasectl."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tomllib
import urllib.error
import urllib.request


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def release_plan():
    raw = read("plan.json")
    return {
        "changed": {name: raw[name] for name in ["desktop", "engine", "backend", "frontend", "support"]},
        "config": tomllib.loads(Path("versions.toml").read_text()),
        "commits": {name: raw[f"{name}_commit"] for name in ["backend", "frontend"]},
        "source_hashes": raw["hashes"],
        "source_commit": run("git", "rev-parse", "HEAD"),
    }


def previous():
    config = tomllib.loads(Path("versions.toml").read_text())
    url = config["desktop"]["updates_url"]
    if not url.startswith("https://"):
        raise ValueError("updates_url must use HTTPS")
    mode = update_trust()
    if mode == "unsigned-development":
        url = development_endpoint(url)
    files = [("", "previous.json")]
    if mode == "signed":
        files.append((".sig", "previous.json.sig"))
    for suffix, name in files:
        try:
            with urllib.request.urlopen(url + suffix, timeout=60) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            if error.code == 404 and not suffix:
                # Missing previous is the explicit first-publication input.
                return
            raise
        Path(name).write_bytes(data)


def plan_outputs():
    plan = release_plan()
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for component, changed in plan["changed"].items():
            if not isinstance(changed, bool):
                raise ValueError("changed values must be booleans")
            build = changed and not (component == "engine" and Path("outputs/engine.json").exists())
            output.write(f"{component}={str(build).lower()}\n")
        output.write(f"changed={str(any(plan['changed'].values())).lower()}\n")
        output.write(f"desktop_version={plan['config']['desktop']['version']}\n")
        output.write(f"engine_version={plan['config']['engine']['version']}\n")


def update_trust():
    mode = os.environ["UPDATE_TRUST"]
    if mode not in ("signed", "unsigned-development"):
        raise ValueError("invalid update trust mode")
    return mode


def configure_update_trust():
    enabled = os.environ.get("UPDATES_SIGNING_ENABLED", "")
    if enabled not in ("", "false", "true"):
        raise ValueError("UPDATES_SIGNING_ENABLED must be true, false or absent")
    mode = "signed" if enabled == "true" else "unsigned-development"
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"mode={mode}\n")
    with open(os.environ["GITHUB_ENV"], "a") as env:
        env.write(f"UPDATE_TRUST={mode}\n")


def development_endpoint(url):
    if not url.endswith(".json"):
        raise ValueError("development updates endpoint must end in .json")
    return url[:-5] + "-unsigned-development.json"


def signing():
    groups = {
        "windows": ("SignPath", ["TOKEN", "ORGANIZATION", "PROJECT", "POLICY"]),
        "macos": ("macOS", ["CERT_P12", "CERT_PASSWORD", "SIGN_IDENTITY",
                          "NOTARY_KEY", "NOTARY_KEY_ID", "NOTARY_ISSUER"]),
    }
    results = {}
    for platform, (name, settings) in groups.items():
        configured = sum(bool(os.environ.get(key)) for key in settings)
        if configured not in (0, len(settings)):
            raise ValueError(f"{name} is partially configured; set all signing settings or none")
        results[platform] = str(configured == len(settings)).lower()
        if not configured:
            print(f"{name} signing is not configured; using unsigned development installers.")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for platform, enabled in results.items():
            output.write(f"{platform}={enabled}\n")


def publication():
    mode = update_trust()
    has_key = bool(os.environ.get("UPDATES_SIGNING_KEY"))
    if has_key != (mode == "signed"):
        raise ValueError("selected update trust requires matching update signing key configuration")
    public = Path("public")
    public.mkdir(exist_ok=True)
    if list(public.iterdir()):
        raise ValueError("publication directory must be empty")
    name = "updates.json" if mode == "signed" else development_endpoint("updates.json")
    target = public / name
    shutil.copyfile("manifest.json", target)
    if mode == "signed":
        run("ci/releasectl", "sign", "--manifest", str(target), "--out", str(target) + ".sig")
        run("ci/releasectl", "verify", "--manifest", str(target), "--signature", str(target) + ".sig")
    else:
        print("Publishing unsigned development metadata (build-time development trust mode).")
        run("ci/releasectl", "verify", "--manifest", str(target), "--unsigned-development")


def reuse_engine():
    """Recover an already published engine after a first-publication failure.

    Only reuse exact source/config hashes and GitHub's immutable asset digests.
    Never rebuild a different source into an existing version.
    """
    plan = release_plan()
    if not plan["changed"]["engine"]:
        return
    repository = os.environ["GITHUB_REPOSITORY"]
    version = plan["config"]["engine"]["version"]
    tag = f"care-engine-v{version}"
    pages = json.loads(run("gh", "api", "--paginate", "--slurp",
                           f"repos/{repository}/releases?per_page=100"))
    existing = [release for page in pages for release in page if release["tag_name"] == tag]
    if not existing:
        return
    release = existing[0]
    source_line = f"Source hash: {plan['source_hashes']['engine']}"
    if (len(existing) != 1 or release["draft"] or release["prerelease"]
            or source_line not in (release.get("body") or "").splitlines()):
        raise ValueError("existing engine release does not match source hash; bump engine version")
    remote = {asset["name"]: asset for asset in release["assets"]}
    assets = {}
    for platform, name in {
        "linux-arm64": f"care-engine-{version}-linux-arm64.tar.gz",
        "linux-amd64": f"care-engine-{version}-linux-amd64.tar.gz",
        "native-linux-arm64": "care-engine-agent-linux-arm64",
        "native-linux-amd64": "care-engine-agent-linux-amd64",
    }.items():
        asset = remote[name]
        digest = asset.get("digest", "")
        expected_url = f"https://github.com/{repository}/releases/download/{tag}/{name}"
        if (not re.fullmatch(r"sha256:(?!0{64}$)[0-9a-f]{64}", digest)
                or asset["size"] <= 0 or asset["browser_download_url"] != expected_url):
            raise ValueError(f"existing engine asset lacks immutable digest/size/URL: {name}")
        assets[platform] = {"url": expected_url, "sha256": digest[7:], "size": asset["size"]}
    write("outputs/engine.json", {"engine": {"version": version, "assets": assets}})


def bootstrap():
    """Resolve both bundle pins before any frontend/Wails build staging.

    A current-run measurement takes precedence; only an unchanged engine may
    use the previously authenticated manifest. Source pins are never a fallback.
    """
    source = Path("versions.toml").read_text()
    config = tomllib.loads(source)
    plan = read("plan.json")
    if Path("outputs/engine.json").exists():
        engine = read("outputs/engine.json")["engine"]
    elif not plan["engine"] and Path("previous.json").exists():
        engine = read("previous.json")["engine"]
    else:
        raise ValueError("engine bootstrap needs measured engine output; source pins are not a fallback")
    version = config["engine"]["version"]
    if engine["version"] != version:
        raise ValueError("engine output version does not match bootstrap configuration")
    repository = os.environ["GITHUB_REPOSITORY"]
    replacements = {}
    for platform, tool in [("linux-arm64", "care_engine_aarch64"), ("linux-amd64", "care_engine_x86_64")]:
        asset = engine["assets"][platform]
        arch = platform.removeprefix("linux-")
        expected_url = (f"https://github.com/{repository}/releases/download/care-engine-v{version}/"
                        f"care-engine-{version}-linux-{arch}.tar.gz")
        if (asset["url"] != expected_url
                or config["tools"][tool]["url"] != expected_url
                or config["tools"][tool]["version"] != version
                or not re.fullmatch(r"(?!0{64}$)[0-9a-f]{64}", asset["sha256"])
                or not isinstance(asset["size"], int) or asset["size"] <= 0):
            raise ValueError(f"invalid measured engine bootstrap asset: {platform}")
        replacements[tool] = asset["sha256"]
    for tool, checksum in replacements.items():
        pattern = rf"(?ms)(^\[tools\.{tool}\]\n)(.*?)(?=^\[|\Z)"
        def replace(match):
            body = re.sub(r"(?m)^sha256\s*=.*\n?", "", match[2])
            return match[1] + f'sha256 = "{checksum}"\n' + body
        source, count = re.subn(pattern, replace, source)
        if count != 1:
            raise ValueError(f"expected exactly one bootstrap configuration section: {tool}")
    Path("versions.toml").write_text(source)


def release_assets(component, publish):
    plan = release_plan()
    version = plan["config"][component]["version"]
    tag = f"v{version}" if component == "desktop" else f"care-engine-v{version}"
    files = sorted(Path("release-assets").glob("*"))
    if not files or any(not p.is_file() or p.stat().st_size == 0 for p in files):
        raise ValueError("release asset set must contain only nonempty files")
    platforms = {
        "desktop": {
            "darwin-universal": f"CARE-Clinic-{version}-macos.dmg",
            "windows-amd64": f"CARE-Clinic-{version}-windows-amd64-setup.exe",
        },
        "engine": {
            "linux-arm64": f"care-engine-{version}-linux-arm64.tar.gz",
            "linux-amd64": f"care-engine-{version}-linux-amd64.tar.gz",
            "native-linux-arm64": "care-engine-agent-linux-arm64",
            "native-linux-amd64": "care-engine-agent-linux-amd64",
        },
    }[component]
    repository = os.environ["GITHUB_REPOSITORY"]
    assets = {}
    for platform, name in platforms.items():
        path = Path("release-assets") / name
        assets[platform] = {
            "url": f"https://github.com/{repository}/releases/download/{tag}/{name}",
            "sha256": digest(path),
            "size": path.stat().st_size,
        }
    checksums = "".join(f"{digest(p)}  {p.name}\n" for p in files)
    Path("release-assets/SHA256SUMS").write_text(checksums)
    write(f"outputs/{component}.json", {component: {"version": version, "assets": assets}})
    if not publish:
        return
    # Never upload to an existing release or overwrite an asset. A matching
    # published release can be reused after a failed Pages deployment.
    releases = json.loads(run("gh", "api", "--paginate", "--slurp",
                              f"repos/{repository}/releases?per_page=100"))
    existing = [r for page in releases for r in page if r["tag_name"] == tag]
    if existing:
        if len(existing) != 1 or existing[0]["draft"]:
            raise ValueError("existing or incomplete release: bump the component version")
        remote = {a["name"]: a for a in existing[0]["assets"]}
        expected = {p.name for p in Path("release-assets").iterdir()}
        if set(remote) != expected:
            raise ValueError("existing immutable release has a different asset set")
        for name, asset in remote.items():
            actual = digest(Path("release-assets") / name)
            if asset.get("digest") != f"sha256:{actual}":
                raise ValueError(f"existing immutable asset differs: {name}")
        return
    run("gh", "release", "create", tag, "--repo", repository, "--draft",
        "--target", plan["source_commit"], "--title", f"CARE {component} {version}",
        "--notes", f"Source: {plan['source_commit']}\nSource hash: "
        f"{plan['source_hashes'][component]}",
        *[str(p) for p in sorted(Path("release-assets").iterdir())])
    run("gh", "release", "edit", tag, "--repo", repository, "--draft=false",
        "--latest=false")


def merge():
    result = read("previous.json") if Path("previous.json").exists() else {"schema": 1, "blocked": []}
    changed = {}
    for path in sorted(Path("outputs").glob("*.json")):
        fragment = read(path)
        for key, value in fragment.items():
            if isinstance(value, dict):
                result.setdefault(key, {})
                if set(changed.get(key, {})) & set(value):
                    raise ValueError(f"duplicate build output in {path}: {key}")
                result[key].update(value)
                changed.setdefault(key, {}).update(value)
            elif key in result:
                raise ValueError(f"duplicate build output: {key}")
            else:
                result[key] = value
    write("outputs.json", result)


RECOVERED_JOBS = {"Prepare atomic update publication": "failure", "Deploy update feed": "skipped"}
FINAL_OUTPUTS = {
    "images": (["output-images"], {"care", "support_images"}),
    "desktop": (["output-desktop"], {"desktop"}),
    "engine": (["output-engine", "output-engine-reused"], {"engine"}),
}


def gh_pages(path, key):
    pages = json.loads(run("gh", "api", "--paginate", "--slurp", path))
    return [item for page in pages for item in page[key]]


def verify_published(repository, component, output, source_hash):
    tag = f"v{output['version']}" if component == "desktop" else f"care-engine-v{output['version']}"
    release = json.loads(run("gh", "api", f"repos/{repository}/releases/tags/{tag}"))
    if (release["draft"] or release["prerelease"]
            or f"Source hash: {source_hash}" not in (release.get("body") or "").splitlines()):
        raise ValueError(f"{tag} does not match the source run's {component} source hash")
    remote = {asset["name"]: asset for asset in release["assets"]}
    prefix = f"https://github.com/{repository}/releases/download/{tag}/"
    for platform, asset in output["assets"].items():
        name = asset["url"].removeprefix(prefix)
        published = remote.get(name)
        if (not asset["url"].startswith(prefix) or published is None
                or published["browser_download_url"] != asset["url"]
                or published.get("digest") != f"sha256:{asset['sha256']}"
                or published["size"] != asset["size"]):
            raise ValueError(f"{component} {platform} output does not match published asset {tag}/{name}")


def recover():
    """Finish publication for a Publish run whose builds and releases succeeded.

    Reuses that run's original plan and final outputs after checking
    provenance; never rebuilds, re-signs installers or relaxes validation.
    """
    repository = os.environ["GITHUB_REPOSITORY"]
    run_id = os.environ["RECOVER_RUN_ID"]
    if not re.fullmatch(r"[1-9][0-9]{0,19}", run_id):
        raise ValueError("recover_run_id must be a workflow run ID")
    mode = update_trust()
    enabled = os.environ.get("UPDATES_SIGNING_ENABLED", "")
    if enabled not in ("", "false", "true") or (enabled == "true") != (mode == "signed"):
        raise ValueError("recovery update trust must match UPDATES_SIGNING_ENABLED")
    for name in ("plan.json", "previous.json", "previous.json.sig", "outputs", "recover"):
        if Path(name).exists():
            raise ValueError("recovery workspace must start clean")

    source = json.loads(run("gh", "api", f"repos/{repository}/actions/runs/{run_id}"))
    if (source["repository"]["full_name"] != repository
            or source["head_repository"]["full_name"] != repository
            or source["event"] != "push" or source["head_branch"] != "main"
            or source["path"] != ".github/workflows/publish.yml"
            or source["status"] != "completed" or source["conclusion"] != "failure"):
        raise ValueError("recovery source must be a failed Publish push run on main of this repository")
    sha = source["head_sha"]
    jobs = gh_pages(f"repos/{repository}/actions/runs/{run_id}/jobs?per_page=100", "jobs")
    for name, conclusion in {**RECOVERED_JOBS, "CI": "success"}.items():
        if [job["conclusion"] for job in jobs if job["name"] == name] != [conclusion]:
            raise ValueError(f"recovery source job {name!r} must have concluded {conclusion}")
    if any(job["conclusion"] not in ("success", "skipped") for job in jobs if job["name"] not in RECOVERED_JOBS):
        raise ValueError("every recovery source build job must have succeeded or been skipped")

    # The failed preparation job recorded which trust environment it used.
    sign_job = next(job["id"] for job in jobs if job["name"] == "Prepare atomic update publication")
    environment = "release-signing" if mode == "signed" else "unsigned-development"
    deployments = json.loads(run("gh", "api",
                                 f"repos/{repository}/deployments?sha={sha}&environment={environment}&per_page=100"))
    log_suffix = f"/actions/runs/{run_id}/job/{sign_job}"
    if not any(status.get("log_url", "").endswith(log_suffix) for deployment in deployments
               for status in json.loads(run("gh", "api", f"repos/{repository}/deployments/{deployment['id']}/statuses"))):
        raise ValueError(f"recovery source run did not prepare publication with {mode} trust")

    run("git", "merge-base", "--is-ancestor", sha, "HEAD")
    if run("git", "diff", "--name-only", sha, "HEAD", "--", "versions.toml"):
        raise ValueError("versions.toml changed since the recovery source run; run the full pipeline")
    Path("recovery-source-sha.txt").write_text(sha + "\n")

    previous()
    current = {name: Path(name).read_bytes() for name in ("previous.json", "previous.json.sig") if Path(name).exists()}
    for name in current:
        Path(name).unlink()
    artifacts = {artifact["name"] for artifact in gh_pages(
        f"repos/{repository}/actions/runs/{run_id}/artifacts?per_page=100", "artifacts") if not artifact["expired"]}

    def download(name, directory):
        run("gh", "run", "download", run_id, "--repo", repository, "--name", name, "--dir", directory)

    download("release-plan", ".")
    original = {name: Path(name).read_bytes() for name in ("previous.json", "previous.json.sig") if Path(name).exists()}
    normalize = os.environ.get("NORMALIZE_DIGEST_REFS", "false") == "true"
    if normalize:
        if mode != "unsigned-development" or original or set(current) != {"previous.json"}:
            raise ValueError("normalization requires an existing unsigned first-publication feed")
        Path("normalization-source.json").write_bytes(current["previous.json"])
    elif original != current:
        raise ValueError("the update feed changed since the recovery source run; run the full pipeline")
    plan = read("plan.json")
    wanted = {"images": plan["backend"] or plan["frontend"] or plan["support"],
              "desktop": plan["desktop"], "engine": plan["engine"]}
    for component, (names, keys) in FINAL_OUTPUTS.items():
        # Only final outputs: per-architecture image fragments are never merged.
        present = [name for name in names if name in artifacts]
        if len(present) != int(bool(wanted[component])):
            raise ValueError(f"recovery source must provide exactly one final {component} output")
        if not present:
            continue
        directory = Path("recover") / present[0]
        download(present[0], str(directory))
        files = sorted(directory.iterdir())
        if [path.name for path in files] != [f"{component}.json"] or set(read(files[0])) != keys:
            raise ValueError(f"unexpected {present[0]} artifact contents")
        write(f"outputs/{component}.json", read(files[0]))
        if component != "images":
            output = read(files[0])[component]
            if output["version"] != plan[f"{component}_version"]:
                raise ValueError(f"{component} output version does not match the source plan")
            verify_published(repository, component, output, plan["hashes"][component])
    shutil.rmtree("recover", ignore_errors=True)


def canonical_digest_refs(manifest):
    refs = [(manifest["support_images"], name) for name in manifest["support_images"]]
    refs += [(manifest["care"][component], field)
             for component in ("backend", "frontend") for field in ("image", "builder")]
    for container, key in refs:
        reference, digest_value = container[key].split("@")
        # A colon after the last slash is a tag, not a registry port.
        if ":" in reference.rsplit("/", 1)[-1]:
            reference = reference.rsplit(":", 1)[0]
        container[key] = reference + "@" + digest_value


def normalize_metadata():
    """Reissue the same measured publication using tag-free digest references."""
    if update_trust() != "unsigned-development":
        raise ValueError("metadata normalization is only supported for unsigned development")
    run("ci/releasectl", "verify", "--manifest", "normalization-source.json", "--unsigned-development")
    current = read("normalization-source.json")
    candidate = read("manifest.json")
    if current["revision"] != 1 or candidate["revision"] != 1:
        raise ValueError("normalization requires source and live first-publication revision 1")
    canonical_digest_refs(candidate)
    canonical_digest_refs(current)
    candidate_at = candidate["published_at"]
    candidate["published_at"] = current["published_at"]
    if candidate != current:
        raise ValueError("live metadata does not match the verified source-run plan and final outputs")
    if read("normalization-source.json") == current:
        raise ValueError("live metadata is already canonical")
    candidate["revision"] += 1
    candidate["published_at"] = candidate_at
    write("manifest.json", candidate)
    run("ci/releasectl", "verify", "--manifest", "manifest.json", "--unsigned-development")
    config = tomllib.loads(Path("versions.toml").read_text())
    endpoint = development_endpoint(config["desktop"]["updates_url"])
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"feed_url={endpoint}\n")
        output.write(f"feed_sha256={digest('normalization-source.json')}\n")


def required():
    jobs = json.loads(os.environ["RESULTS"])
    if not jobs or any(job["result"] != "success" for job in jobs.values()):
        raise ValueError(f"required checks did not all succeed: {jobs}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["previous", "plan-outputs", "assets", "merge", "required",
                                          "signing", "publication", "reuse-engine", "trust", "bootstrap",
                                          "recover", "normalize-metadata"])
    parser.add_argument("--component", choices=["desktop", "engine"])
    parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    if args.command == "assets":
        release_assets(args.component, args.publish)
    else:
        {"previous": previous, "plan-outputs": plan_outputs,
         "merge": merge, "required": required, "signing": signing,
         "publication": publication, "reuse-engine": reuse_engine,
         "trust": configure_update_trust, "bootstrap": bootstrap,
         "recover": recover, "normalize-metadata": normalize_metadata}[args.command]()


if __name__ == "__main__":
    main()
