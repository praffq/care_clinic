import contextlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("build_images", Path(__file__).with_name("build-images.py"))
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class ImageBuildTests(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, {
            "GITHUB_REPOSITORY": "org/repo", "GITHUB_RUN_ID": "123",
            "GITHUB_RUN_ATTEMPT": "1",
        })
        self.env.start()
        self.addCleanup(self.env.stop)

    def test_each_build_targets_only_its_native_architecture(self):
        for arch in ["amd64", "arm64"]:
            with patch.dict(os.environ, {"IMAGE_ARCH": arch}):
                with patch.object(images.subprocess, "run") as run:
                    self.assertEqual(images.build("care-frontend", "images/frontend", "Dockerfile", False), "")
                    command = run.call_args.args[0]
                    self.assertEqual(command[command.index("--platform") + 1], f"linux/{arch}")
                    self.assertIn(f"ghcr.io/org/repo/care-frontend:run-123-1-{arch}", command)
                    self.assertIn("type=cacheonly", command)
        with patch.dict(os.environ, {"IMAGE_ARCH": "unknown"}):
            with self.assertRaises(ValueError):
                images.build("care-frontend", ".", "Dockerfile", False)

    def test_assembly_requires_both_native_platforms_and_immutable_refs(self):
        refs = [f"ghcr.io/org/repo/care-frontend@sha256:{c * 64}" for c in ["a", "b"]]
        manifests = [
            {"manifests": [{"platform": {"os": "linux", "architecture": arch}},
                           {"platform": {"os": "unknown", "architecture": "unknown"}}]}
            for arch in ["amd64", "arm64"]
        ]
        manifests.append({"digest": "sha256:" + "c" * 64,
                          "manifests": [m["manifests"][0] for m in manifests]})
        with patch.object(images, "run", side_effect=[json.dumps(m) for m in manifests]):
            with patch.object(images.subprocess, "run") as run:
                result = images.assemble_image("care-frontend", refs)
                self.assertEqual(result, "ghcr.io/org/repo/care-frontend@sha256:" + "c" * 64)
                self.assertEqual(run.call_args.args[0][-2:], refs)
        with patch.object(images, "run", return_value=json.dumps(manifests[0])):
            with patch.object(images.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "native arm64"):
                    images.assemble_image("care-frontend", refs)
                run.assert_not_called()
        with self.assertRaisesRegex(ValueError, "invalid native"):
            images.assemble_image("care-frontend", [refs[0], "ghcr.io/other/image:tag"])
        with self.assertRaisesRegex(ValueError, "both native"):
            images.assemble_image("care-frontend", refs[:1])
        manifests[-1]["manifests"] = manifests[-1]["manifests"][:1]
        with patch.object(images, "run", side_effect=[json.dumps(m) for m in manifests]):
            with patch.object(images.subprocess, "run"):
                with self.assertRaisesRegex(ValueError, "assembled index"):
                    images.assemble_image("care-frontend", refs)

    def test_release_output_keeps_frontend_settings_and_both_builders(self):
        plan = {
            "changed": {"backend": True, "frontend": True, "support": True},
            "commits": {"backend": "b" * 40, "frontend": "f" * 40},
            "source_hashes": {"backend": "be-hash", "frontend": "fe-hash"},
        }
        with tempfile.TemporaryDirectory(dir=".") as directory, contextlib.chdir(directory):
            for arch in ["amd64", "arm64"]:
                care = {}
                for name in ["backend", "frontend"]:
                    care[name] = {
                        "commit": plan["commits"][name], "source_hash": plan["source_hashes"][name],
                        "image": f"{name}-{arch}", "builder": f"{name}-builder-{arch}",
                    }
                care["frontend"]["build_env"] = {"REACT_EMPTY": "", "REACT_TITLE": "$literal"}
                images.write(f"outputs/{arch}/images.json", {
                    "care": care, "support_images": {
                        "caddy": f"caddy-{arch}", "backup": f"backup-{arch}",
                        "postgres": "pg@digest", "redis": "redis@digest", "silo": "silo@digest",
                    },
                })
            with patch.object(images, "release_plan", return_value=plan):
                with patch.object(images, "assemble_image", side_effect=lambda name, refs: name + "@index") as assemble:
                    images.assemble()
                    self.assertEqual(assemble.call_count, 6)
                    result = images.read("outputs/images.json")
                    self.assertEqual(result["care"]["frontend"]["build_env"], {"REACT_EMPTY": "", "REACT_TITLE": "$literal"})
                    self.assertEqual(result["care"]["frontend"]["builder"], "care-frontend-builder@index")
                    self.assertEqual(result["support_images"]["backup"], "care-backup@index")
                arm = images.read("outputs/arm64/images.json")
                arm["care"]["frontend"]["commit"] = "wrong"
                images.write("outputs/arm64/images.json", arm)
                with patch.object(images, "assemble_image"):
                    with self.assertRaisesRegex(ValueError, "release plan"):
                        images.assemble()
                Path("outputs/arm64/images.json").unlink()
                with self.assertRaises(FileNotFoundError):
                    images.assemble()


if __name__ == "__main__":
    unittest.main()
