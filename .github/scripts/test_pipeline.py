import contextlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import pipeline


class PipelineTests(unittest.TestCase):
    def test_required_checks_reject_failure_cancellation_and_skips(self):
        for name in ["plan", "care", "engine", "desktop"]:
            for result in ["success", "failure", "cancelled", "skipped"]:
                jobs = {key: {"result": "success"} for key in ["plan", "care", "engine", "desktop"]}
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
