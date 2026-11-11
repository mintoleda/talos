#!/usr/bin/env python3
"""Run GPUI's daemon-bridge acceptance against a temporary ACP fixture home.

Build first: RUST_FONTCONFIG_DLOPEN=1 cargo test -p talos-gpui --no-run && cargo build -p talosd -p talosdctl -p talos-acp --bins
No real provider credentials or homes are accessed. This does not test GUI input.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("talosd_smoke", ROOT / "scripts/talosd-smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def main():
    harness = smoke.Harness(argparse.Namespace(verbose=True, keep=False))
    try:
        daemon = harness.start_daemon("gpui")
        # Discover the exact test executable from Cargo metadata, not a stale glob.
        metadata = subprocess.run(
            ["cargo", "test", "-p", "talos-gpui", "--no-run", "--message-format=json"],
            cwd=ROOT, capture_output=True, text=True, check=True,
        )
        executable = next(
            artifact["executable"] for line in metadata.stdout.splitlines()
            if (artifact := json.loads(line)).get("reason") == "compiler-artifact"
            and artifact.get("target", {}).get("name") == "talos-gpui"
            and artifact.get("profile", {}).get("test") and artifact.get("executable")
        )
        env = harness.child_env(daemon.home)
        env["TALOS_GPUI_TEST_ROOT"] = str(harness.root)
        subprocess.run([executable, "--ignored", "--exact", "acceptance::daemon_workflow", "--nocapture"],
                       cwd=ROOT, env=env, timeout=180, check=True)
        harness.stop_daemon(daemon, graceful=True)
        harness.check_fixture_cleanup("gpui")
    finally:
        harness.cleanup()


if __name__ == "__main__":
    main()
