#!/usr/bin/env python3
"""Build native CARE images and assemble immutable multi-architecture indexes."""

import argparse
import json
import os
from pathlib import Path
import subprocess
from pipeline import read, release_plan, run, write


def build(name, context, dockerfile, publish, target=None, args=None, contexts=None):
    arch = os.environ["IMAGE_ARCH"]
    if arch not in {"amd64", "arm64"}:
        raise ValueError("IMAGE_ARCH must be amd64 or arm64")
    tag = image_tag(name) + "-" + arch
    metadata = f"ci/{name}.json"
    command = ["docker", "buildx", "build", "--platform", f"linux/{arch}",
               "--file", dockerfile, "--tag", tag, "--metadata-file", metadata]
    if target:
        command += ["--target", target]
    for key, value in (args or {}).items():
        command += ["--build-arg", f"{key}={value}"]
    for key, value in (contexts or {}).items():
        command += ["--build-context", f"{key}={value}"]
    command += ["--push"] if publish else ["--output", "type=cacheonly"]
    command += [context]
    subprocess.run(command, check=True)
    if not publish:
        return ""
    image_digest = read(metadata)["containerimage.digest"]
    if not valid_digest(image_digest):
        raise ValueError("BuildKit did not return a valid image digest")
    return tag.split(":")[0] + "@" + image_digest


def valid_digest(value):
    return isinstance(value, str) and value.startswith("sha256:") and len(value) == 71 and all(
        c in "0123456789abcdef" for c in value[7:])


def image_tag(name):
    repository = os.environ["GITHUB_REPOSITORY"].lower()
    return f"ghcr.io/{repository}/{name}:run-{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}"


def assemble_image(name, refs):
    if len(refs) != 2:
        raise ValueError(f"{name} requires both native image outputs")
    repository = image_tag(name).split(":")[0]
    for ref in refs:
        if not isinstance(ref, str) or not ref.startswith(repository + "@") or not valid_digest(ref.split("@")[-1]):
            raise ValueError(f"invalid native image reference for {name}")
    # Platform metadata comes from BuildKit's native outputs, not guessed tags.
    platforms = set()
    for arch, ref in zip(["amd64", "arm64"], refs, strict=True):
        manifest = json.loads(run("docker", "buildx", "imagetools", "inspect", ref,
                                  "--format", "{{json .Manifest}}"))
        actual = {(m.get("platform", {}).get("os"), m.get("platform", {}).get("architecture"))
                  for m in manifest.get("manifests", [])}
        native = actual & {("linux", "amd64"), ("linux", "arm64")}
        if native != {("linux", arch)}:
            raise ValueError(f"{name} output does not match its native {arch} job")
        platforms.update(native)
    if platforms != {("linux", "amd64"), ("linux", "arm64")}:
        raise ValueError(f"{name} is missing a native target architecture")
    tag = image_tag(name)
    subprocess.run(["docker", "buildx", "imagetools", "create", "--tag", tag, *refs], check=True)
    manifest = json.loads(run("docker", "buildx", "imagetools", "inspect", tag,
                              "--format", "{{json .Manifest}}"))
    actual = {(m.get("platform", {}).get("os"), m.get("platform", {}).get("architecture"))
              for m in manifest.get("manifests", [])}
    if actual & {("linux", "amd64"), ("linux", "arm64")} != platforms:
        raise ValueError(f"{name} assembled index is missing a target architecture")
    digest = manifest["digest"]
    if not valid_digest(digest):
        raise ValueError("BuildKit did not return a valid multi-architecture digest")
    return repository + "@" + digest


def assemble():
    plan = release_plan()
    results = [read(f"outputs/{arch}/images.json") for arch in ["amd64", "arm64"]]
    result = {"care": {}}
    for name in ["backend", "frontend"]:
        if not plan["changed"][name]:
            continue
        entries = [r["care"][name] for r in results]
        metadata = [{k: v for k, v in entry.items() if k not in {"image", "builder"}} for entry in entries]
        if (metadata[0] != metadata[1]
                or metadata[0].get("commit") != plan["commits"][name]
                or metadata[0].get("source_hash") != plan["source_hashes"][name]):
            raise ValueError(f"native {name} outputs do not match the release plan")
        result["care"][name] = {
            **metadata[0],
            "image": assemble_image(f"care-{name}", [e["image"] for e in entries]),
            "builder": assemble_image(f"care-{name}-builder", [e["builder"] for e in entries]),
        }
    if plan["changed"]["support"]:
        support = [r["support_images"] for r in results]
        if any(support[0][name] != support[1][name] for name in ["postgres", "redis", "silo"]):
            raise ValueError("native builds resolved different upstream support images")
        result["support_images"] = {
            **{name: support[0][name] for name in ["postgres", "redis", "silo"]},
            **{name: assemble_image(f"care-{name}", [s[name] for s in support]) for name in ["caddy", "backup"]},
        }
    write("outputs/images.json", result)


def checkout(source, commit, directory):
    if len(commit) != 40 or any(c not in "0123456789abcdef" for c in commit):
        raise ValueError("upstream builds require a resolved full commit")
    repo = source["repo"]
    if len(repo.split("/")) != 2 or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-" for c in repo):
        raise ValueError("upstream repository must be owner/repo")
    subprocess.run(["git", "init", directory], check=True)
    subprocess.run(["git", "-C", directory, "remote", "add", "origin", f"https://github.com/{repo}.git"], check=True)
    subprocess.run(["git", "-C", directory, "fetch", "--depth=1", "origin", commit], check=True)
    subprocess.run(["git", "-C", directory, "checkout", "--detach", "FETCH_HEAD"], check=True)
    if run("git", "-C", directory, "rev-parse", "HEAD") != commit:
        raise ValueError("checkout did not match the planned branch head")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--publish", action="store_true")
    parser.add_argument("--assemble", action="store_true")
    args = parser.parse_args()
    if args.assemble:
        assemble()
        return
    plan = release_plan()
    config = plan["config"]
    Path("ci").mkdir(exist_ok=True)
    care = {}
    for name in ["backend", "frontend"]:
        if not plan["changed"][name]:
            continue
        commit = plan["commits"][name]
        directory = f"ci/care-{name}"
        checkout(config["care"][name], commit, directory)
        if name == "backend":
            dockerfile = f"{directory}/docker/prod.Dockerfile"
            # Upstream CARE provides its runtime and plugin builder in the
            # same Dockerfile; both are built from exactly the resolved SHA.
            image = build("care-backend", directory, dockerfile, args.publish)
            builder = build("care-backend-builder", directory, dockerfile, args.publish, target="builder")
            build_env = None
        else:
            subprocess.run(["node", ".github/scripts/frontend-env.mjs"], check=True)
            build_env = read("ci/frontend-build.json")["build_env"]
            context = {"care_fe": directory}
            image = build("care-frontend", "images/frontend", "images/frontend/Dockerfile",
                          args.publish, contexts=context)
            builder = build("care-frontend-builder", "images/frontend", "images/frontend/Dockerfile",
                            args.publish, target="builder", contexts=context)
        if args.publish:
            care[name] = {"commit": commit, "image": image, "builder": builder,
                          "source_hash": plan["source_hashes"][name]}
            if build_env is not None:
                care[name]["build_env"] = build_env
    result = {"care": care}
    if plan["changed"]["support"]:
        upstream = config["support_images"]
        caddy = build("care-caddy", "deployments", "deployments/caddy.Dockerfile", args.publish,
                     args={"CADDY_IMAGE": upstream["caddy"], "CORAZA_VERSION": config["caddy"]["coraza"]})
        backup = build("care-backup", "deployments", "deployments/backup.Dockerfile", args.publish,
                      args={"POSTGRES_IMAGE": upstream["postgres"]})
        if args.publish:
            support = {"caddy": caddy, "backup": backup}
            for name in ["postgres", "redis", "silo"]:
                ref = upstream[name]
                if "@" not in ref:
                    details = run("docker", "buildx", "imagetools", "inspect", ref,
                                  "--format", "{{json .Manifest}}")
                    digest = json.loads(details)["digest"]
                    ref = ref + "@" + digest
                support[name] = ref
            result["support_images"] = support
    if args.publish:
        write("outputs/images.json", result)


if __name__ == "__main__":
    main()
