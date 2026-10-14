#!/usr/bin/env python3
"""Build immutable multi-architecture CARE/runtime builder and support images."""

import argparse
import json
import os
from pathlib import Path
import subprocess
from pipeline import read, release_plan, run, write


def build(name, context, dockerfile, publish, target=None, args=None, contexts=None):
    repository = os.environ["GITHUB_REPOSITORY"].lower()
    tag = f"ghcr.io/{repository}/{name}:run-{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}"
    metadata = f"ci/{name}.json"
    command = ["docker", "buildx", "build", "--platform", "linux/amd64,linux/arm64",
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
    if not image_digest.startswith("sha256:") or len(image_digest) != 71:
        raise ValueError("BuildKit did not return a valid image digest")
    return tag.split(":")[0] + "@" + image_digest


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
    args = parser.parse_args()
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
            subprocess.run(["node", "images/scripts/frontend-env.mjs", "--out",
                            "images/frontend/clinic.env", "--json", "ci/frontend-build.json"], check=True)
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
