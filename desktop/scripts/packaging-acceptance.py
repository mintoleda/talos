#!/usr/bin/env python3
"""Exercise Linux user install, upgrade, and uninstall in isolated homes."""

from __future__ import annotations

import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from typing import Iterable


DESKTOP_ROOT = Path(__file__).resolve().parents[1]
BINARIES = ("talosd", "talosdctl", "talos-desktop")
INSTALLED_FILES = (
    ".local/bin/talosd",
    ".local/bin/talosdctl",
    ".local/bin/talos-desktop",
    ".config/systemd/user/talosd.service",
    ".local/share/applications/talos-desktop.desktop",
)


class AcceptanceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AcceptanceFailure(message)


def run(
    command: list[str],
    *,
    cwd: Path | None = None,
    home: Path | None = None,
    expect_success: bool = True,
) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    if home is not None:
        env["HOME"] = str(home)
        env.pop("TALOSD_HOME", None)
    result = subprocess.run(
        command,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=30,
        check=False,
    )
    if expect_success and result.returncode != 0:
        raise AcceptanceFailure(
            f"Command failed ({result.returncode}): {' '.join(command)}\n{result.stdout}"
        )
    if not expect_success and result.returncode == 0:
        raise AcceptanceFailure(
            f"Command unexpectedly succeeded: {' '.join(command)}\n{result.stdout}"
        )
    return result


def write_fixture_bundle(root: Path, version: str) -> tuple[Path, Path]:
    bundle = root / f"bundle-{version}"
    bundle.mkdir(parents=True)
    for binary in BINARIES:
        path = bundle / binary
        path.write_text(
            "#!/bin/sh\n"
            f"printf '%s\\n' '{binary} fixture {version}'\n",
            encoding="utf-8",
        )
        path.chmod(0o755)

    packaging = bundle / "packaging"
    shutil.copytree(DESKTOP_ROOT / "packaging", packaging)

    archive = root / f"talos-desktop-linux-x86_64-{version}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        for path in sorted(bundle.rglob("*")):
            tar.add(path, arcname=path.relative_to(bundle))

    checksum = archive.with_name(archive.name + ".sha256")
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    checksum.write_text(f"{digest}  {archive.name}\n", encoding="ascii")
    run(["sha256sum", "--check", "--status", checksum.name], cwd=root)
    verify_checksum_rejects_tampering(root, archive, checksum)

    extracted = root / f"extracted-{version}"
    extracted.mkdir()
    extract_checked_archive(archive, extracted)
    for binary in BINARIES:
        require((extracted / binary).is_file(), f"Archive is missing {binary}")
    require(
        (extracted / "packaging" / "uninstall-user.sh").is_file(),
        "Archive is missing packaging/uninstall-user.sh",
    )
    return extracted, checksum


def verify_checksum_rejects_tampering(root: Path, archive: Path, checksum: Path) -> None:
    damaged_dir = root / f"tampered-{archive.stem}"
    damaged_dir.mkdir()
    damaged_archive = damaged_dir / archive.name
    damaged_archive.write_bytes(archive.read_bytes() + b"tampered")
    damaged_checksum = damaged_dir / checksum.name
    damaged_checksum.write_bytes(checksum.read_bytes())
    run(
        ["sha256sum", "--check", "--status", damaged_checksum.name],
        cwd=damaged_dir,
        expect_success=False,
    )


def extract_checked_archive(archive: Path, destination: Path) -> None:
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        for member in members:
            member_path = PurePosixPath(member.name)
            require(
                not member_path.is_absolute() and ".." not in member_path.parts,
                f"Unsafe archive member path: {member.name}",
            )
            require(
                member.isfile() or member.isdir(),
                f"Unsupported archive member type: {member.name}",
            )
        tar.extractall(destination, members=members)


def install(bundle: Path, home: Path) -> None:
    run(["bash", "packaging/install-user.sh"], cwd=bundle, home=home)


def uninstall(bundle: Path, home: Path) -> None:
    run(["bash", "packaging/uninstall-user.sh"], cwd=bundle, home=home)


def assert_installed(home: Path, version: str) -> None:
    for binary in BINARIES:
        path = home / ".local" / "bin" / binary
        require(path.is_file(), f"Installed binary is missing: {path}")
        require(
            path.read_text(encoding="utf-8").endswith(f"{binary} fixture {version}'\n"),
            f"Installed {binary} is not from fixture version {version}",
        )
        require(stat.S_IMODE(path.stat().st_mode) == 0o755, f"Wrong mode on {path}")

    service = home / ".config/systemd/user/talosd.service"
    desktop = home / ".local/share/applications/talos-desktop.desktop"
    require(
        service.read_bytes()
        == (DESKTOP_ROOT / "packaging/systemd/talosd.service").read_bytes(),
        "Installed systemd unit differs from the release bundle",
    )
    require(
        desktop.read_bytes()
        == (DESKTOP_ROOT / "packaging/desktop/talos-desktop.desktop").read_bytes(),
        "Installed desktop entry differs from the release bundle",
    )
    require(stat.S_IMODE(service.stat().st_mode) == 0o644, "Wrong systemd unit mode")
    require(stat.S_IMODE(desktop.stat().st_mode) == 0o644, "Wrong desktop entry mode")


def snapshot(paths: Iterable[Path]) -> dict[Path, bytes]:
    result: dict[Path, bytes] = {}
    for path in paths:
        require(path.is_file(), f"Expected retained data is missing: {path}")
        result[path] = path.read_bytes()
    return result


def assert_snapshot(expected: dict[Path, bytes], phase: str) -> None:
    for path, contents in expected.items():
        require(path.is_file(), f"{phase} removed retained data: {path}")
        require(path.read_bytes() == contents, f"{phase} changed retained data: {path}")


def exercise_install_lifecycle(root: Path, v1: Path, v2: Path) -> None:
    home = root / "fresh-home"
    home.mkdir()

    install(v1, home)
    assert_installed(home, "v1")
    require(not (home / ".talosd").exists(), "Fresh install created daemon data")
    require(not (home / ".paseo").exists(), "Fresh install created or migrated legacy data")

    data = home / ".talosd"
    data.mkdir()
    (data / "config.toml").write_bytes(b"daemon = 'retained'\n")
    (data / "auth.token").write_bytes(b"fixture-secret-token\n")
    (data / "talosd.sqlite3").write_bytes(b"fixture-database-bytes\n")
    legacy = home / ".paseo"
    legacy.mkdir()
    (legacy / "legacy-marker").write_bytes(b"leave legacy data alone\n")

    local_keep = home / ".local/bin/user-tool"
    local_keep.write_bytes(b"user-owned executable\n")
    systemd_keep = home / ".config/systemd/user/other.service"
    systemd_keep.write_bytes(b"user-owned service\n")
    app_keep = home / ".local/share/applications/other.desktop"
    app_keep.write_bytes(b"user-owned desktop entry\n")
    retained = snapshot(
        [
            data / "config.toml",
            data / "auth.token",
            data / "talosd.sqlite3",
            legacy / "legacy-marker",
            local_keep,
            systemd_keep,
            app_keep,
        ]
    )

    install(v2, home)
    assert_installed(home, "v2")
    assert_snapshot(retained, "Upgrade")

    uninstall(v2, home)
    for relative in INSTALLED_FILES:
        require(not (home / relative).exists(), f"Uninstall left package file: {relative}")
    assert_snapshot(retained, "Uninstall")


def exercise_symlink_guards(root: Path, bundle: Path) -> None:
    outside = root / "outside"
    outside.mkdir()
    outside_marker = outside / "keep.txt"
    outside_marker.write_bytes(b"outside target remains unchanged\n")

    linked_home = root / "linked-home"
    linked_home.mkdir()
    (linked_home / ".local").symlink_to(outside, target_is_directory=True)
    result = run(
        ["bash", "packaging/install-user.sh"],
        cwd=bundle,
        home=linked_home,
        expect_success=False,
    )
    require("Refusing symlink destination" in result.stdout, "Installer did not reject symlink parent")
    require(outside_marker.read_bytes() == b"outside target remains unchanged\n", "Installer modified symlink target")

    uninstall_home = root / "uninstall-symlink-home"
    uninstall_home.mkdir()
    install(bundle, uninstall_home)
    installed_binary = uninstall_home / ".local/bin/talosd"
    installed_binary.unlink()
    installed_binary.symlink_to(outside_marker)
    result = run(
        ["bash", "packaging/uninstall-user.sh"],
        cwd=bundle,
        home=uninstall_home,
        expect_success=False,
    )
    require("Refusing symlink destination" in result.stdout, "Uninstaller did not reject symlink target")
    require(installed_binary.is_symlink(), "Uninstaller removed the refused symlink")
    require(outside_marker.read_bytes() == b"outside target remains unchanged\n", "Uninstaller modified symlink target")
    require(
        (uninstall_home / ".local/bin/talosdctl").is_file(),
        "Uninstaller partially removed files before refusing a symlink",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--verbose", action="store_true", help="print each acceptance phase")
    args = parser.parse_args()
    if os.geteuid() == 0:
        print("Run this acceptance check as an unprivileged user; the installer intentionally rejects root.", file=sys.stderr)
        return 2
    if sys.platform != "linux":
        print("This acceptance check requires Linux (sha256sum and the Linux user installer).", file=sys.stderr)
        return 2
    if shutil.which("sha256sum") is None:
        print("sha256sum is required.", file=sys.stderr)
        return 2

    try:
        with tempfile.TemporaryDirectory(prefix="talos-packaging-acceptance-") as temporary:
            root = Path(temporary)
            v1, _ = write_fixture_bundle(root, "v1")
            v2, _ = write_fixture_bundle(root, "v2")
            if args.verbose:
                print("PASS: fixture archives verified; modified checksums were rejected")
            exercise_install_lifecycle(root, v1, v2)
            if args.verbose:
                print("PASS: fresh install, upgrade, uninstall, and daemon/legacy data retention")
            exercise_symlink_guards(root, v1)
            if args.verbose:
                print("PASS: installer and uninstaller refused symlink destinations without partial changes")
    except (AcceptanceFailure, OSError, tarfile.TarError, subprocess.SubprocessError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1

    print("Linux packaging acceptance passed using temporary fixture bundles and homes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
