import contextlib
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest
from unittest.mock import patch

import pipeline


class PipelineTests(unittest.TestCase):
    def test_nonsecret_selector_determines_build_trust_before_protected_signing(self):
        for value, mode in [("", "unsigned-development"), ("false", "unsigned-development"),
                            ("true", "signed"), ("typo", None)]:
            with tempfile.TemporaryDirectory(dir=".") as directory:
                output, env = Path(directory) / "output", Path(directory) / "env"
                with patch.dict(os.environ, {"UPDATES_SIGNING_ENABLED": value,
                                            "GITHUB_OUTPUT": str(output), "GITHUB_ENV": str(env)}, clear=True):
                    if mode is None:
                        with self.assertRaises(ValueError):
                            pipeline.configure_update_trust()
                    else:
                        pipeline.configure_update_trust()
                        self.assertEqual(output.read_text(), f"mode={mode}\n")
                        self.assertEqual(env.read_text(), f"UPDATE_TRUST={mode}\n")

    def test_installer_signing_all_or_none_like_original_windows_release(self):
        groups = {
            "windows": ["TOKEN", "ORGANIZATION", "PROJECT", "POLICY"],
            "macos": ["CERT_P12", "CERT_PASSWORD", "SIGN_IDENTITY",
                      "NOTARY_KEY", "NOTARY_KEY_ID", "NOTARY_ISSUER"],
        }
        with tempfile.TemporaryDirectory(dir=".") as directory:
            output = Path(directory) / "output"
            for platform, keys in groups.items():
                for count in range(len(keys) + 1):
                    output.write_text("")
                    env = {key: "configured" for key in keys[:count]}
                    env["GITHUB_OUTPUT"] = str(output)
                    with patch.dict(os.environ, env, clear=True):
                        if count in (0, len(keys)):
                            pipeline.signing()
                            self.assertIn(f"{platform}={str(count > 0).lower()}", output.read_text())
                        else:
                            with self.assertRaisesRegex(ValueError, "partially configured"):
                                pipeline.signing()
                            self.assertEqual(output.read_text(), "")

    def test_publication_matches_build_trust_and_never_falls_back_after_signing_failure(self):
        for mode, key in [("signed", "key"), ("unsigned-development", "")]:
            with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
                Path("manifest.json").write_text("manifest bytes")
                with patch.dict(os.environ, {"UPDATE_TRUST": mode, "UPDATES_SIGNING_KEY": key}):
                    with patch.object(pipeline, "run") as run:
                        pipeline.publication()
                        calls = [call.args for call in run.call_args_list]
                        if key:
                            self.assertEqual([call[1] for call in calls], ["sign", "verify"])
                            self.assertIn("--signature", calls[1])
                            self.assertEqual(Path("public/updates.json").read_text(), "manifest bytes")
                        else:
                            self.assertEqual(len(calls), 1)
                            self.assertIn("--unsigned-development", calls[0])
                            self.assertEqual([p.name for p in Path("public").iterdir()],
                                             ["updates-unsigned-development.json"])
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            Path("manifest.json").write_text("manifest bytes")
            with patch.dict(os.environ, {"UPDATE_TRUST": "signed", "UPDATES_SIGNING_KEY": "key"}):
                with patch.object(pipeline, "run", side_effect=ValueError("untrusted key")):
                    with self.assertRaisesRegex(ValueError, "untrusted key"):
                        pipeline.publication()
                    self.assertFalse(Path("public/updates-unsigned-development.json").exists())
        for mode, key in [("signed", ""), ("unsigned-development", "key"), ("invalid", "")]:
            with patch.dict(os.environ, {"UPDATE_TRUST": mode, "UPDATES_SIGNING_KEY": key}):
                with self.assertRaises(ValueError):
                    pipeline.publication()

    def test_previous_feed_has_explicit_matching_trust(self):
        for mode in ["signed", "unsigned-development"]:
            with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
                Path("versions.toml").write_text('[desktop]\nupdates_url = "https://example.test/updates.json"\n')
                response = unittest.mock.MagicMock()
                response.__enter__.return_value.read.return_value = b"manifest"
                with patch.dict(os.environ, {"UPDATE_TRUST": mode}):
                    with patch.object(pipeline.urllib.request, "urlopen", return_value=response) as fetch:
                        pipeline.previous()
                urls = [call.args[0] for call in fetch.call_args_list]
                expected = ["https://example.test/updates.json", "https://example.test/updates.json.sig"]
                if mode == "unsigned-development":
                    expected = ["https://example.test/updates-unsigned-development.json"]
                self.assertEqual(urls, expected)

    def test_reuse_engine_requires_exact_provenance_digests_and_bootstrap_pins(self):
        version, source_hash = "0.1.3", "a" * 64
        base = f"https://github.com/org/repo/releases/download/care-engine-v{version}/"
        names = [f"care-engine-{version}-linux-{arch}.tar.gz" for arch in ["arm64", "amd64"]]
        names += [f"care-engine-agent-linux-{arch}" for arch in ["arm64", "amd64"]]
        assets = [{"name": name, "digest": "sha256:" + "b" * 64, "size": 42,
                   "browser_download_url": base + name} for name in names]
        release = {"tag_name": f"care-engine-v{version}", "body": f"Source hash: {source_hash}",
                   "draft": False, "prerelease": False, "assets": assets}
        plan = {"changed": {"engine": True}, "source_hashes": {"engine": source_hash},
                "config": {"engine": {"version": version}, "tools": {
                    "care_engine_aarch64": {"url": base + names[0], "sha256": "b" * 64},
                    "care_engine_x86_64": {"url": base + names[1], "sha256": "b" * 64},
                }}}
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            with patch.object(pipeline, "release_plan", return_value=plan):
                with patch.dict(os.environ, {"GITHUB_REPOSITORY": "org/repo"}):
                    with patch.object(pipeline, "run", return_value=json.dumps([[release]])) as run:
                        pipeline.reuse_engine()
                        self.assertEqual(run.call_count, 1)  # read-only API only
                    self.assertEqual(len(pipeline.read("outputs/engine.json")["engine"]["assets"]), 4)
                    for field, bad in [("body", "Source hash: wrong"), ("draft", True), ("prerelease", True),
                                       ("assets", []), ("assets", [dict(a, digest="sha256:" + "0" * 64) for a in assets])]:
                        invalid = dict(release, **{field: bad})
                        with patch.object(pipeline, "run", return_value=json.dumps([[invalid]])):
                            with self.assertRaises((ValueError, KeyError)):
                                pipeline.reuse_engine()

    def test_reused_engine_skips_rebuild_but_still_publishes_complete_feed(self):
        plan = {"changed": {name: True for name in ["desktop", "engine", "backend", "frontend", "support"]},
                "config": {"desktop": {"version": "0.0.1"}, "engine": {"version": "0.1.3"}}}
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            pipeline.write("outputs/engine.json", {"engine": {"assets": {}}})
            with patch.object(pipeline, "release_plan", return_value=plan):
                with patch.dict(os.environ, {"GITHUB_OUTPUT": "output"}):
                    pipeline.plan_outputs()
            output = Path("output").read_text()
            self.assertIn("engine=false\n", output)
            self.assertIn("desktop=true\n", output)
            self.assertIn("changed=true\n", output)

    def test_clean_bootstrap_embeds_current_measurements_for_both_architectures(self):
        root = Path.cwd()
        source = Path("versions.toml").read_text()
        config = tomllib.loads(source)
        version = config["engine"]["version"]
        plan = {"config": config}
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            Path("versions.toml").write_text(source)
            pipeline.write("plan.json", {"engine": True})
            Path("release-assets").mkdir()
            for name in [f"care-engine-{version}-linux-arm64.tar.gz",
                         f"care-engine-{version}-linux-amd64.tar.gz",
                         "care-engine-agent-linux-arm64", "care-engine-agent-linux-amd64"]:
                Path("release-assets", name).write_text("new synthetic engine bytes: " + name)
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": "praffq/care_clinic"}):
                with patch.object(pipeline, "release_plan", return_value=plan):
                    with patch.object(pipeline, "run") as run:
                        pipeline.release_assets("engine", False)
                        run.assert_not_called()  # PR validation measures without publishing.
                measured = pipeline.read("outputs/engine.json")["engine"]
                pipeline.bootstrap()
                resolved = tomllib.loads(Path("versions.toml").read_text())
                for arch, tool in [("arm64", "care_engine_aarch64"), ("amd64", "care_engine_x86_64")]:
                    actual = pipeline.digest(Path("release-assets", f"care-engine-{version}-linux-{arch}.tar.gz"))
                    self.assertEqual(resolved["tools"][tool]["sha256"], actual)
                    self.assertEqual(measured["assets"][f"linux-{arch}"]["sha256"], actual)
                staged = json.loads(subprocess.check_output([
                    "go", "-C", str(root / "app"), "run", "./cmd/stageconfig",
                    "-config", str(Path("versions.toml").resolve()),
                    "-env", str(root / "deployments/.env"),
                ], text=True))
                self.assertEqual(staged["desktop_version"], "0.0.1")
                for arch, env in [("arm64", "CARE_VM_AARCH64_SHA256"), ("amd64", "CARE_VM_X86_64_SHA256")]:
                    self.assertIn(f'{env}={measured["assets"][f"linux-{arch}"]["sha256"]}\n', staged["env"])
                resolved_once = Path("versions.toml").read_text()
                pipeline.bootstrap()
                self.assertEqual(Path("versions.toml").read_text(), resolved_once)

    def test_bootstrap_never_falls_back_to_missing_stale_or_invalid_measurements(self):
        source = Path("versions.toml").read_text()
        base = "https://github.com/praffq/care_clinic/releases/download/care-engine-v0.1.3/"
        engine = {"version": "0.1.3", "assets": {
            f"linux-{arch}": {"url": base + f"care-engine-0.1.3-linux-{arch}.tar.gz",
                             "sha256": checksum * 64, "size": 42}
            for arch, checksum in [("arm64", "a"), ("amd64", "b")]
        }}
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            Path("versions.toml").write_text(source)
            pipeline.write("plan.json", {"engine": True})
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": "praffq/care_clinic"}):
                with self.assertRaisesRegex(ValueError, "needs measured engine output"):
                    pipeline.bootstrap()
                pipeline.write("previous.json", {"engine": engine})
                with self.assertRaises(ValueError):
                    pipeline.bootstrap()  # A changed engine cannot reuse previous metadata.
                for mutate in [
                    lambda m: m.update(version="0.1.2"),
                    lambda m: m["assets"].pop("linux-amd64"),
                    lambda m: m["assets"]["linux-arm64"].update(sha256="0" * 64),
                    lambda m: m["assets"]["linux-amd64"].update(size=0),
                    lambda m: m["assets"]["linux-amd64"].update(url=base + "wrong"),
                ]:
                    invalid = copy.deepcopy(engine)
                    mutate(invalid)
                    pipeline.write("outputs/engine.json", {"engine": invalid})
                    with self.assertRaises((ValueError, KeyError)):
                        pipeline.bootstrap()
                    self.assertEqual(Path("versions.toml").read_text(), source)
                Path("outputs/engine.json").unlink()
                pipeline.write("plan.json", {"engine": False})
                pipeline.bootstrap()  # Unchanged engine uses authenticated previous metadata.
                resolved = tomllib.loads(Path("versions.toml").read_text())
                self.assertEqual(resolved["tools"]["care_engine_aarch64"]["sha256"], "a" * 64)
                self.assertEqual(resolved["tools"]["care_engine_x86_64"]["sha256"], "b" * 64)

    def recovery_fixture(self):
        repo, sha, run_id = "org/repo", "c" * 40, "123"
        digest = "d" * 64
        desktop = {"desktop": {"version": "0.0.1", "assets": {"darwin-universal": {
            "url": f"https://github.com/{repo}/releases/download/v0.0.1/app.dmg", "sha256": digest, "size": 9}}}}
        engine = {"engine": {"version": "0.1.3", "assets": {"linux-amd64": {
            "url": f"https://github.com/{repo}/releases/download/care-engine-v0.1.3/e.tar.gz",
            "sha256": digest, "size": 7}}}}
        images = {"care": {"final": True}, "support_images": {"silo": "final"}}
        state = {
            "run": {"repository": {"full_name": repo}, "head_repository": {"full_name": repo},
                    "event": "push", "head_branch": "main", "path": ".github/workflows/publish.yml",
                    "status": "completed", "conclusion": "failure", "head_sha": sha},
            "jobs": [{"name": "Engine checks / Build engine amd64", "conclusion": "success", "id": 1},
                     {"name": "Desktop checks / Sign Windows", "conclusion": "skipped", "id": 2},
                     {"name": "CI", "conclusion": "success", "id": 3},
                     {"name": "Prepare atomic update publication", "conclusion": "failure", "id": 4},
                     {"name": "Deploy update feed", "conclusion": "skipped", "id": 5}],
            "deployment_env": "unsigned-development",
            "log_url": f"https://github.com/{repo}/actions/runs/{run_id}/job/4",
            "versions_diff": "",
            "plan": {"desktop": True, "engine": True, "backend": True, "frontend": False, "support": False,
                     "desktop_version": "0.0.1", "engine_version": "0.1.3",
                     "hashes": {"desktop": "a" * 64, "engine": "b" * 64}},
            "artifacts": {"release-plan": None, "output-images": ("images.json", images),
                          "output-images-amd64": ("images.json", {"care": {"amd64": True}, "support_images": {}}),
                          "output-desktop": ("desktop.json", desktop), "output-engine": ("engine.json", engine)},
            "releases": {"v0.0.1": ("a" * 64, desktop), "care-engine-v0.1.3": ("b" * 64, engine)},
            "feed": None,
        }

        def fake_run(*args):
            if args[:2] == ("gh", "api"):
                path = args[-1]
                if path.endswith(f"/runs/{run_id}"):
                    return json.dumps(state["run"])
                if "/jobs?" in path:
                    return json.dumps([{"jobs": state["jobs"]}])
                if "/artifacts?" in path:
                    return json.dumps([{"artifacts": [{"name": n, "expired": False} for n in state["artifacts"]]}])
                if "/deployments?" in path:
                    env = path.split("environment=")[1].split("&")[0]
                    return json.dumps([{"id": 9}] if env == state["deployment_env"] else [])
                if path.endswith("/deployments/9/statuses"):
                    return json.dumps([{"log_url": state["log_url"]}])
                if "/releases/tags/" in path:
                    tag = path.rsplit("/", 1)[1]
                    source_hash, output = state["releases"][tag]
                    component = next(iter(output))
                    return json.dumps({"draft": False, "prerelease": False, "body": f"Source hash: {source_hash}",
                                       "assets": [{"name": a["url"].rsplit("/", 1)[1], "browser_download_url": a["url"],
                                                   "digest": "sha256:" + a["sha256"], "size": a["size"]}
                                                  for a in output[component]["assets"].values()]})
            if args[:2] == ("git", "merge-base"):
                return ""
            if args[:2] == ("git", "diff"):
                return state["versions_diff"]
            if args[:3] == ("gh", "run", "download"):
                name, directory = args[args.index("--name") + 1], Path(args[args.index("--dir") + 1])
                if name == "release-plan":
                    pipeline.write(directory / "plan.json", state["plan"])
                else:
                    filename, value = state["artifacts"][name]
                    pipeline.write(directory / filename, value)
                return ""
            raise AssertionError(args)

        def fake_previous():
            if state["feed"] is not None:
                Path("previous.json").write_text(state["feed"])

        env = {"GITHUB_REPOSITORY": repo, "RECOVER_RUN_ID": run_id, "UPDATE_TRUST": "unsigned-development"}
        return state, fake_run, fake_previous, env, (images, desktop, engine)

    def test_recovery_reuses_verified_final_outputs_of_failed_main_run(self):
        state, fake_run, fake_previous, env, (images, desktop, engine) = self.recovery_fixture()
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            with patch.dict(os.environ, env), patch.object(pipeline, "run", side_effect=fake_run), \
                    patch.object(pipeline, "previous", side_effect=fake_previous):
                pipeline.recover()
            self.assertEqual(pipeline.read("plan.json"), state["plan"])
            self.assertEqual(sorted(p.name for p in Path("outputs").iterdir()),
                             ["desktop.json", "engine.json", "images.json"])
            # The final multi-arch index wins; per-arch fragments are never merged.
            self.assertEqual(pipeline.read("outputs/images.json"), images)
            self.assertEqual(pipeline.read("outputs/desktop.json"), desktop)
            self.assertEqual(pipeline.read("outputs/engine.json"), engine)
            self.assertFalse(Path("recover").exists())

    def test_recovery_rejects_untrusted_incomplete_or_changed_sources(self):
        def mutate(path, value):
            def apply(state):
                target = state
                for key in path[:-1]:
                    target = target[key]
                target[path[-1]] = value
            return apply

        def bad_digest(state):
            tag, (source_hash, output) = "v0.0.1", state["releases"]["v0.0.1"]
            changed = copy.deepcopy(output)
            changed["desktop"]["assets"]["darwin-universal"]["sha256"] = "e" * 64
            state["releases"][tag] = (source_hash, changed)

        cases = {
            "other repository": mutate(["run", "head_repository", "full_name"], "fork/repo"),
            "non-main branch": mutate(["run", "head_branch"], "feature"),
            "non-push event": mutate(["run", "event"], "workflow_dispatch"),
            "other workflow": mutate(["run", "path"], ".github/workflows/other.yml"),
            "successful run": mutate(["run", "conclusion"], "success"),
            "failed build": mutate(["jobs", 0, "conclusion"], "failure"),
            "cancelled build": mutate(["jobs", 1, "conclusion"], "cancelled"),
            "CI not green": mutate(["jobs", 2, "conclusion"], "skipped"),
            "deploy already ran": mutate(["jobs", 4, "conclusion"], "success"),
            "other trust": mutate(["deployment_env"], "release-signing"),
            "other job deployment": mutate(["log_url"], "https://github.com/org/repo/actions/runs/123/job/40"),
            "versions changed": mutate(["versions_diff"], "versions.toml"),
            "feed changed": mutate(["feed"], "{}"),
            "missing final images": lambda s: s["artifacts"].pop("output-images"),
            "duplicate engine": mutate(["artifacts", "output-engine-reused"], ("engine.json", {"engine": {}})),
            "release source hash": lambda s: s["releases"].update(
                {"care-engine-v0.1.3": ("f" * 64, s["releases"]["care-engine-v0.1.3"][1])}),
            "asset digest": bad_digest,
            "version mismatch": mutate(["plan", "desktop_version"], "0.0.2"),
        }
        for name, change in cases.items():
            with self.subTest(name), tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
                state, fake_run, fake_previous, env, _ = self.recovery_fixture()
                change(state)
                with patch.dict(os.environ, env), patch.object(pipeline, "run", side_effect=fake_run), \
                        patch.object(pipeline, "previous", side_effect=fake_previous):
                    with self.assertRaises(ValueError):
                        pipeline.recover()
        for name, extra in {"bad run id": {"RECOVER_RUN_ID": "1;rm"},
                            "signed without selector": {"UPDATE_TRUST": "signed"},
                            "selector without signed": {"UPDATES_SIGNING_ENABLED": "true"}}.items():
            with self.subTest(name), tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
                state, fake_run, fake_previous, env, _ = self.recovery_fixture()
                with patch.dict(os.environ, {**env, **extra}), patch.object(pipeline, "run", side_effect=fake_run) as run:
                    with self.assertRaises(ValueError):
                        pipeline.recover()
                    run.assert_not_called()

    def test_required_checks_reject_failure_cancellation_and_skips(self):
        for name in ["plan", "care", "engine", "desktop", "desktop-package"]:
            for result in ["success", "failure", "cancelled", "skipped"]:
                jobs = {key: {"result": "success"} for key in ["plan", "care", "engine", "desktop", "desktop-package"]}
                jobs[name]["result"] = result
                with patch.dict(os.environ, {"RESULTS": json.dumps(jobs)}):
                    if result == "success":
                        pipeline.required()
                    else:
                        with self.assertRaises(ValueError):
                            pipeline.required()

    def test_manifest_merge_retains_unchanged_outputs(self):
        with tempfile.TemporaryDirectory(dir=".") as directory:
            with contextlib.chdir(directory):
                pipeline.write("previous.json", {
                    "schema": 1, "care": {"backend": {"image": "old-be"}, "frontend": {"image": "old-fe"}},
                    "desktop": {"assets": {"darwin-universal": {"url": "old"}}},
                    "support_images": {"redis": "old-redis"},
                })
                pipeline.write("outputs/images.json", {
                    "care": {"frontend": {"image": "new-fe"}},
                    "support_images": {"redis": "new-redis"},
                })
                pipeline.merge()
                result = pipeline.read("outputs.json")
                self.assertEqual(result["care"]["backend"]["image"], "old-be")
                self.assertEqual(result["care"]["frontend"]["image"], "new-fe")
                self.assertEqual(result["desktop"]["assets"]["darwin-universal"]["url"], "old")
                self.assertEqual(result["support_images"]["redis"], "new-redis")

    def test_assets_hash_every_file_and_record_size(self):
        with tempfile.TemporaryDirectory(dir=".") as directory:
            with contextlib.chdir(directory):
                Path("release-assets").mkdir()
                names = ["CARE-Clinic-1.2.3-macos.dmg", "CARE-Clinic-1.2.3-windows-amd64-setup.exe", "versions.toml"]
                for name in names:
                    Path("release-assets", name).write_text(name)
                plan = {"config": {"desktop": {"version": "1.2.3"}}}
                with patch.object(pipeline, "release_plan", return_value=plan):
                    with patch.dict(os.environ, {"GITHUB_REPOSITORY": "org/repo"}):
                        pipeline.release_assets("desktop", False)
                checksums = Path("release-assets/SHA256SUMS").read_text()
                for name in names:
                    self.assertIn(f"{pipeline.digest(Path('release-assets', name))}  {name}\n", checksums)
                assets = pipeline.read("outputs/desktop.json")["desktop"]["assets"]
                self.assertEqual(assets["darwin-universal"]["size"], len(names[0]))
                self.assertIn("/releases/download/v1.2.3/", assets["darwin-universal"]["url"])

    def test_existing_assets_never_overwritten(self):
        with tempfile.TemporaryDirectory(dir=".") as directory:
            with contextlib.chdir(directory):
                Path("release-assets").mkdir()
                for name in ["CARE-Clinic-1.2.3-macos.dmg", "CARE-Clinic-1.2.3-windows-amd64-setup.exe"]:
                    Path("release-assets", name).write_text("binary")
                plan = {"config": {"desktop": {"version": "1.2.3"}}}
                existing = [[{"tag_name": "v1.2.3", "draft": False, "assets": []}]]
                with patch.object(pipeline, "release_plan", return_value=plan):
                    with patch.object(pipeline, "run", return_value=json.dumps(existing)) as run:
                        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "org/repo"}):
                            with self.assertRaisesRegex(ValueError, "different asset set"):
                                pipeline.release_assets("desktop", True)
                        self.assertEqual(run.call_count, 1)

    def test_engine_manifest_ships_native_agents_with_bundles(self):
        with tempfile.TemporaryDirectory(dir=".") as directory:
            with contextlib.chdir(directory):
                Path("release-assets").mkdir()
                names = [
                    "care-engine-0.1.3-linux-arm64.tar.gz",
                    "care-engine-0.1.3-linux-amd64.tar.gz",
                    "care-engine-agent-linux-arm64",
                    "care-engine-agent-linux-amd64",
                ]
                for name in names:
                    Path("release-assets", name).write_text(name)
                plan = {"config": {"engine": {"version": "0.1.3"}}}
                with patch.object(pipeline, "release_plan", return_value=plan):
                    with patch.dict(os.environ, {"GITHUB_REPOSITORY": "org/repo"}):
                        pipeline.release_assets("engine", False)
                assets = pipeline.read("outputs/engine.json")["engine"]["assets"]
                self.assertEqual(set(assets), {
                    "linux-arm64", "linux-amd64", "native-linux-arm64", "native-linux-amd64",
                })
                for arch in ["arm64", "amd64"]:
                    asset = assets[f"native-linux-{arch}"]
                    name = f"care-engine-agent-linux-{arch}"
                    self.assertEqual(asset["sha256"], pipeline.digest(Path("release-assets", name)))
                    self.assertEqual(asset["size"], len(name))
                    self.assertIn(f"/care-engine-v0.1.3/{name}", asset["url"])
                Path("release-assets/care-engine-agent-linux-amd64").unlink()
                with patch.object(pipeline, "release_plan", return_value=plan):
                    with patch.dict(os.environ, {"GITHUB_REPOSITORY": "org/repo"}):
                        with self.assertRaises(FileNotFoundError):
                            pipeline.release_assets("engine", False)


if __name__ == "__main__":
    unittest.main()
