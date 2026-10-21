#!/usr/bin/env python3
"""Check the October 20 backend-resource change on immutable source profiles.

All downloads, fixture injection, Go execution and captures use fresh scratch
directories. The optional candidate is read-only. No Git state is changed.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

import check as regression

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
PROFILES = {
    "before": "stack-14817-before-backend-listeners",
    "pr14717": "stack-14817-2fe5-backend-closure",
    "tip": "stack-14817-e9436e-backend-closure",
    "histories": "stack-14817-e9436e-histories",
    "rewarming": "stack-14817-e9436e-rewarming",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--source", type=Path, help="read-only repaired candidate")
    parser.add_argument("--expect-fixed", action="store_true")
    parser.add_argument("--no-live", action="store_true", help=argparse.SUPPRESS)  # Compatibility with existing CI commands.
    args = parser.parse_args()
    if bool(args.source) != args.expect_fixed:
        parser.error("candidate runs require both --source and --expect-fixed")
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="backend-closure-"))).resolve()
    require(out != ROOT and ROOT not in out.parents, "artifacts must be outside the research repository")
    if args.source:
        require(args.source.resolve() != out and args.source.resolve() not in out.parents, "artifacts must be outside the candidate")
    out.mkdir(parents=True, exist_ok=True)
    require(not any(out.iterdir()), "artifacts must be fresh")
    print(f"Backend closure artifacts: {out}", flush=True)
    harness_paths = [HERE / "backend_closure.py", HERE / "check.py", HERE / "profiles.json",
                     HERE / "fixtures/backend_resource_carry_test.go.txt",
                     HERE / "fixtures/backend_emission_graph_test.go.txt"]
    harness_hashes = {str(path.relative_to(ROOT)): regression.digest(path) for path in harness_paths}
    captures = out / "captures"
    receipt = {"profiles": {}, "harness_sha256": harness_hashes,
               "evidence_level": "pinned Go/KRT/publisher checks; no live Envoy replay",
               "limits": ["fixture translated backend rows", "no full Kubernetes deployment", "no runtime application observation"]}
    for label, name in PROFILES.items():
        directory = out / label
        command = ["python3", "-B", str(HERE / "check.py"), "--profile", name, "--artifacts", str(directory)]
        if args.source and label in ("tip", "histories", "rewarming"):
            command += ["--source", str(args.source.resolve()), "--expect-fixed"]
        env = dict(os.environ)
        if label == "tip":
            env["REVIEW_BACKEND_OUT"] = str(captures)
        with (out / (label+".log")).open("w") as log:
            subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        receipt["profiles"][label] = json.loads((directory / "receipt.json").read_text())
        print("PASS", label, flush=True)
    require(harness_hashes == {str(path.relative_to(ROOT)): regression.digest(path) for path in harness_paths},
            "harness sources changed during verification")
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2)+"\n")
    print("PASS designated backend-closure outcomes;", out, flush=True)


if __name__ == "__main__":
    main()
