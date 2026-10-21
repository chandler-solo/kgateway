import json
import io
from contextlib import redirect_stdout
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import backend_closure as closure


class BackendClosureTests(unittest.TestCase):
    def test_preserves_existing_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "keep.json"
            marker.write_text(json.dumps({"owner": "user"}))
            with patch("sys.argv", ["backend_closure.py", "--artifacts", directory]), patch("backend_closure.subprocess.run") as run, self.assertRaises(ValueError):
                closure.main()
            run.assert_not_called()
            self.assertEqual({"owner": "user"}, json.loads(marker.read_text()))

    def test_artifacts_cannot_be_inside_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            artifacts = Path(directory) / "artifacts"
            with patch("sys.argv", ["backend_closure.py", "--source", directory, "--expect-fixed", "--artifacts", str(artifacts)]), patch("backend_closure.subprocess.run") as run, self.assertRaises(ValueError):
                closure.main()
            run.assert_not_called()
            self.assertFalse(artifacts.exists())


class PublicProfileTests(unittest.TestCase):
    def test_source_and_runtime_profiles_use_public_subjects(self):
        root = Path(__file__).resolve().parents[1]
        profiles = json.loads((root / "regressions/profiles.json").read_text())
        self.assertEqual(set(profiles), {"schema", "profiles"})
        for name, profile in profiles["profiles"].items():
            with self.subTest(profile=name):
                self.assertIn(profile["repository"], {"kgateway-dev/kgateway", "chandler-solo/kgateway"})
        for suite in ("runtime", "live", "timeouts"):
            images = json.loads((root / suite / "profiles.json").read_text())["images"]
            self.assertTrue(images)
            for name, profile in images.items():
                with self.subTest(suite=suite, image=name):
                    self.assertTrue(profile["image"].startswith("envoyproxy/envoy:"))
                    self.assertIn("@sha256:", profile["image"])

    def test_backend_gate_runs_source_profiles_without_runtime(self):
        def run(command, **kwargs):
            out = Path(command[command.index("--artifacts") + 1])
            out.mkdir()
            (out / "receipt.json").write_text(json.dumps({"profile": command[command.index("--profile") + 1]}))
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "run"
            with patch("sys.argv", ["backend_closure.py", "--artifacts", str(out)]), patch("backend_closure.subprocess.run", side_effect=run) as execution, redirect_stdout(io.StringIO()):
                closure.main()
            self.assertEqual(execution.call_count, len(closure.PROFILES))
            evidence = json.loads((out / "receipt.json").read_text())
            self.assertEqual(set(evidence["profiles"]), set(closure.PROFILES))
            self.assertIn("no live Envoy replay", evidence["evidence_level"])


if __name__ == "__main__":
    unittest.main()
