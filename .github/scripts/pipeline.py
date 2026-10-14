#!/usr/bin/env python3
"""Small CI adapters; release policy and manifest generation live in releasectl."""

import argparse
import hashlib
import json
import os
from pathlib import Path
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
    for suffix, name in [("", "previous.json"), (".sig", "previous.json.sig")]:
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
            output.write(f"{component}={str(changed).lower()}\n")
        output.write(f"changed={str(any(plan['changed'].values())).lower()}\n")
        output.write(f"desktop_version={plan['config']['desktop']['version']}\n")
        output.write(f"engine_version={plan['config']['engine']['version']}\n")


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
    write(f"outputs/{component}.json", {component: {"assets": assets}})
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


def required():
    jobs = json.loads(os.environ["RESULTS"])
    if not jobs or any(job["result"] != "success" for job in jobs.values()):
        raise ValueError(f"required checks did not all succeed: {jobs}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["previous", "plan-outputs", "assets", "merge", "required"])
    parser.add_argument("--component", choices=["desktop", "engine"])
    parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    if args.command == "assets":
        release_assets(args.component, args.publish)
    else:
        {"previous": previous, "plan-outputs": plan_outputs,
         "merge": merge, "required": required}[args.command]()


if __name__ == "__main__":
    main()
