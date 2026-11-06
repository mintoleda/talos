#!/usr/bin/env python3
"""Exercise Linux user install, upgrade, and uninstall in isolated homes."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import signal
import stat
import subprocess
import sys
import time
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
    extra_env: dict[str, str] | None = None,
    expect_success: bool = True,
) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    if home is not None:
        env["HOME"] = str(home)
        env.pop("TALOSD_HOME", None)
    if extra_env is not None:
        env.update(extra_env)
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
            tar.add(path, arcname=path.relative_to(bundle), recursive=False)

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


def validate_systemd_unit(service: Path) -> None:
    expected = {
        "Unit": {"Description": "Talos desktop daemon (per-user)"},
        "Service": {
            "Type": "simple",
            "ExecStart": "%h/.local/bin/talosd",
            "KillSignal": "SIGINT",
            "KillMode": "control-group",
            "TimeoutStopSec": "15s",
            "Restart": "on-failure",
            "RestartSec": "3s",
        },
        "Install": {"WantedBy": "default.target"},
    }
    directives: dict[str, dict[str, str]] = {}
    section: str | None = None
    for line_number, line in enumerate(service.read_text(encoding="utf-8").splitlines(), 1):
        value = line.strip()
        if not value or value.startswith(("#", ";")):
            continue
        if value.startswith("[") and value.endswith("]"):
            section = value[1:-1]
            continue
        require("=" in value, f"Malformed systemd directive at {service}:{line_number}")
        key, directive_value = (part.strip() for part in value.split("=", 1))
        if section in expected and key in expected[section]:
            section_directives = directives.setdefault(section, {})
            require(key not in section_directives, f"Duplicate systemd directive [{section}] {key}")
            section_directives[key] = directive_value

    for unit_section, required in expected.items():
        for key, value in required.items():
            require(
                directives.get(unit_section, {}).get(key) == value,
                f"Packaged systemd unit requires [{unit_section}] {key}={value}",
            )


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
    validate_systemd_unit(service)
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


def report_systemd_user_manager() -> None:
    systemctl = shutil.which("systemctl")
    if systemctl is None:
        print("INFO: systemd user manager unavailable: systemctl executable not found; no service actions attempted.")
        return

    try:
        result = subprocess.run(
            [systemctl, "--user", "is-system-running"],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=5,
            check=False,
        )
    except subprocess.TimeoutExpired:
        print("INFO: systemd user manager status check timed out; no service actions attempted.")
        return

    state = result.stdout.strip() or f"systemctl exited {result.returncode} without output"
    if result.returncode == 0:
        print(f"INFO: systemd user manager is reachable ({state}); no service actions attempted.")
    elif state == "offline" or any(
        marker in state.lower()
        for marker in ("failed to connect to bus", "no medium found", "not been booted with systemd")
    ):
        print(f"INFO: systemd user manager unavailable ({state}); no service actions attempted.")
    else:
        print(f"INFO: systemd user manager status is {state}; no service actions attempted.")


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


def write_compiled_bundle(root: Path) -> Path:
    release = DESKTOP_ROOT / "target/release"
    bundle = root / "compiled-release-bundle"
    bundle.mkdir()
    for binary in BINARIES:
        source = release / binary
        require(source.is_file() and os.access(source, os.X_OK), f"Missing compiled release binary: {source}")
        shutil.copy2(source, bundle / binary)
    shutil.copytree(DESKTOP_ROOT / "packaging", bundle / "packaging")
    archive = root / "talos-desktop-linux-x86_64-compiled.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        for path in sorted(bundle.rglob("*")):
            tar.add(path, arcname=path.relative_to(bundle), recursive=False)
    checksum = archive.with_name(archive.name + ".sha256")
    checksum.write_text(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n", encoding="ascii")
    run(["sha256sum", "--check", "--status", checksum.name], cwd=root)
    extracted = root / "compiled-extracted"
    extracted.mkdir()
    extract_checked_archive(archive, extracted)
    return extracted


def ctl_json(ctl: Path, home: Path, extra_env: dict[str, str], *args: str) -> dict:
    result = run(
        [str(ctl), "--home", str(home / ".talosd"), "--json", *args],
        home=home,
        extra_env=extra_env,
    )
    try:
        return json.loads(result.stdout)
    except ValueError as error:
        raise AcceptanceFailure(f"CLI did not return JSON for {args}: {result.stdout}") from error


def exercise_compiled_cli(bundle: Path, root: Path) -> None:
    home = root / "compiled-home"
    home.mkdir()
    install(bundle, home)
    daemon_bin = home / ".local/bin/talosd"
    ctl = home / ".local/bin/talosdctl"
    daemon_home = home / ".talosd"
    private_tmp = root / "compiled-tmp"
    xdg_config = home / ".config"
    xdg_data = home / ".local/share"
    xdg_cache = home / ".cache"
    xdg_runtime = root / "compiled-runtime"
    for path in (private_tmp, xdg_config, xdg_data, xdg_cache, xdg_runtime):
        path.mkdir(parents=True, exist_ok=True)
    empty_gitconfig = root / "empty-gitconfig"
    empty_gitconfig.touch()
    env = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(home),
        "TALOSD_HOME": str(daemon_home),
        "TMPDIR": str(private_tmp),
        "XDG_CONFIG_HOME": str(xdg_config),
        "XDG_DATA_HOME": str(xdg_data),
        "XDG_CACHE_HOME": str(xdg_cache),
        "XDG_RUNTIME_DIR": str(xdg_runtime),
        "GIT_CONFIG_GLOBAL": str(empty_gitconfig),
        "GIT_CONFIG_NOSYSTEM": "1",
        "RUST_LOG": "talosd=debug",
    }
    processes: list[subprocess.Popen[bytes]] = []
    log_files = []
    child_pid: int | None = None

    def start_daemon(label: str) -> tuple[subprocess.Popen[bytes], Path]:
        log_path = root / f"{label}.log"
        log_file = log_path.open("wb")
        log_files.append(log_file)
        process = subprocess.Popen(
            [str(daemon_bin)],
            env=env,
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        processes.append(process)
        return process, log_path

    def wait_for_health(process: subprocess.Popen[bytes], log_path: Path) -> None:
        for _ in range(100):
            if process.poll() is not None:
                raise AcceptanceFailure(
                    f"Compiled daemon exited early; log: {log_path.read_text(errors='replace')}"
                )
            try:
                run(
                    [str(ctl), "--home", str(daemon_home), "health"],
                    home=home,
                    extra_env=env,
                )
                return
            except AcceptanceFailure:
                time.sleep(0.1)
        raise AcceptanceFailure(
            f"Installed compiled CLI could not reach daemon; log: {log_path.read_text(errors='replace')}"
        )

    try:
        daemon, log_path = start_daemon("compiled-daemon")
        wait_for_health(daemon, log_path)

        repo = root / "compiled-repo"
        repo.mkdir()
        subprocess.run(["git", "init", "--quiet", "-b", "main"], cwd=repo, env=env, check=True)
        subprocess.run(["git", "config", "user.name", "Talos Artifact Test"], cwd=repo, env=env, check=True)
        subprocess.run(["git", "config", "user.email", "artifact-test@example.invalid"], cwd=repo, env=env, check=True)
        (repo / "tracked.txt").write_text("release fixture\\n", encoding="utf-8")
        subprocess.run(["git", "add", "tracked.txt"], cwd=repo, env=env, check=True)
        subprocess.run(["git", "commit", "--quiet", "-m", "initial"], cwd=repo, env=env, check=True)
        project = ctl_json(ctl, home, env, "project", "create", "Artifact project", str(repo))
        worktree = ctl_json(ctl, home, env, "worktree", "create", project["id"], "Artifact worktree", "feature/artifact", "HEAD")
        require(Path(worktree["path"]).is_dir(), "Installed CLI did not create its managed worktree")
        deleted = ctl_json(ctl, home, env, "worktree", "delete", worktree["id"])
        preserved_path = Path(deleted["path"])
        require(
            deleted["lifecycle"] == "Archived"
            and deleted["metadata"]["managedWorktree"]["preserved"] is True
            and deleted["metadata"]["managedWorktree"]["path"] == str(preserved_path),
            f"Installed CLI did not report the archived, preserved worktree path: {deleted!r}",
        )
        require(
            preserved_path.is_dir()
            and (preserved_path / "tracked.txt").read_text(encoding="utf-8")
            == "release fixture\\n",
            "Managed worktree delete did not retain checkout contents at its reported path",
        )
        require(
            not Path(worktree["path"]).exists(),
            "Managed worktree delete left the original checkout path in place",
        )
        association_repo = root / "association-repo"
        association_repo.mkdir()
        association_workspace_path = association_repo / "workspace"
        association_workspace_path.mkdir()
        association_project = ctl_json(
            ctl,
            home,
            env,
            "project",
            "create",
            "Artifact association project",
            str(association_repo),
        )
        association_workspace = ctl_json(
            ctl,
            home,
            env,
            "workspace",
            "add",
            "Artifact association workspace",
            str(association_workspace_path),
        )
        ctl_json(
            ctl,
            home,
            env,
            "workspace",
            "set-project",
            association_workspace["id"],
            association_project["id"],
        )
        ctl_json(
            ctl,
            home,
            env,
            "workspace",
            "set-project",
            association_workspace["id"],
            "none",
        )
        ctl_json(ctl, home, env, "project", "delete", association_project["id"])
        workspace = ctl_json(ctl, home, env, "workspace", "add", "Artifact terminal", str(repo))
        terminal = ctl_json(ctl, home, env, "terminal", "create", workspace["id"], "--program", "sh", "--arg=-c", "--arg=sleep 30 & echo $!; wait")
        child_pid = None
        for _ in range(50):
            capture = ctl_json(ctl, home, env, "terminal", "capture", workspace["id"], terminal["id"])
            child_pid = next((int(line.strip()) for line in capture.get("data", "").splitlines() if line.strip().isdigit()), None)
            if child_pid is not None:
                break
            time.sleep(0.1)
        require(child_pid is not None, "Compiled PTY did not report a descendant PID")
        daemon.kill()
        daemon.wait(timeout=5)
        for _ in range(50):
            stat_path = Path(f"/proc/{child_pid}/stat")
            try:
                fields = stat_path.read_text(encoding="ascii").rsplit(")", 1)[1].strip().split()
                alive = bool(fields and fields[0] != "Z")
            except OSError:
                alive = False
            if not alive:
                break
            time.sleep(0.1)
        require(not alive, f"Compiled daemon hard crash orphaned PTY descendant {child_pid}")

        lock_path = daemon_home / "talosd.lock"
        socket_path = daemon_home / "talosd.sock"
        require(lock_path.is_file(), "Hard daemon crash did not leave the expected instance marker")
        require(socket_path.is_socket(), "Hard daemon crash did not leave the expected Unix socket")
        crashed_marker = lock_path.read_text(encoding="ascii")
        require(
            crashed_marker.startswith(f"talosd pid={daemon.pid} id="),
            "Crash marker does not identify the daemon that was terminated",
        )

        restarted, restarted_log = start_daemon("compiled-restarted-daemon")
        wait_for_health(restarted, restarted_log)
        restarted_marker = lock_path.read_text(encoding="ascii")
        require(
            restarted_marker.startswith(f"talosd pid={restarted.pid} id="),
            "Restart did not replace the stale daemon marker",
        )
        require(
            restarted_marker != crashed_marker,
            "Restart reused the previous daemon instance marker",
        )
        restarted.send_signal(signal.SIGINT)
        restarted.wait(timeout=10)
        require(restarted.returncode == 0, "Restarted daemon did not stop cleanly")
        require(not lock_path.exists(), "Graceful restart shutdown left an instance marker")
        require(not socket_path.exists(), "Graceful restart shutdown left a Unix socket")
        require(
            not list(daemon_home.glob(".talosd.lock.tmp-*")),
            "Crash recovery left a staged instance marker",
        )

        installed_bins = {binary: hashlib.sha256((home / ".local/bin" / binary).read_bytes()).hexdigest() for binary in BINARIES}
        # Leave an older executable at every installed binary path, then verify
        # that the compiled release bundle replaces it as an upgrade would.
        for binary in BINARIES:
            old_version = home / ".local/bin" / binary
            old_version.write_text(f"#!/bin/sh\nprintf '%s\\n' '{binary} previous release'\n", encoding="utf-8")
            old_version.chmod(0o755)
        service = home / ".config/systemd/user/talosd.service"
        desktop_entry = home / ".local/share/applications/talos-desktop.desktop"
        service.write_bytes(b"previous service unit\n")
        desktop_entry.write_bytes(b"previous desktop entry\n")
        install(bundle, home)
        for binary, digest in installed_bins.items():
            require(hashlib.sha256((home / ".local/bin" / binary).read_bytes()).hexdigest() == digest, f"Compiled upgrade did not restore {binary}")
        require(
            service.read_bytes() == (bundle / "packaging/systemd/talosd.service").read_bytes(),
            "Compiled upgrade did not replace the systemd unit",
        )
        require(
            desktop_entry.read_bytes()
            == (bundle / "packaging/desktop/talos-desktop.desktop").read_bytes(),
            "Compiled upgrade did not replace the desktop entry",
        )
        retained = [path for path in daemon_home.rglob("*") if path.is_file()]
        require(bool(retained), "Compiled CLI workflow created no durable daemon files")
        before = snapshot(retained)
        uninstall(bundle, home)
        for relative in INSTALLED_FILES:
            require(not (home / relative).exists(), f"Compiled uninstall left {relative}")
        assert_snapshot(before, "Compiled uninstall")
    finally:
        for process in processes:
            if process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGINT)
                    process.wait(timeout=5)
                except (ProcessLookupError, subprocess.TimeoutExpired):
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait(timeout=5)
        if child_pid is not None:
            try:
                stat_fields = Path(f"/proc/{child_pid}/stat").read_text(encoding="ascii").rsplit(")", 1)[1].strip().split()
                if stat_fields and stat_fields[0] != "Z":
                    os.kill(child_pid, signal.SIGKILL)
            except (OSError, ProcessLookupError):
                pass
        for log_file in log_files:
            log_file.close()


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
    parser.add_argument("--compiled-artifact", action="store_true", help="also install a bundle made from target/release binaries and run real CLI workflows")
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

    report_systemd_user_manager()

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
            if args.compiled_artifact:
                real_bundle = write_compiled_bundle(root)
                exercise_compiled_cli(real_bundle, root)
                if args.verbose:
                    print("PASS: compiled release bundle install, CLI/worktree workflow, stale-marker restart, upgrade replacement, and uninstall retention")
    except (AcceptanceFailure, OSError, tarfile.TarError, subprocess.SubprocessError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1

    print("Linux packaging acceptance passed using temporary fixture bundles and homes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
