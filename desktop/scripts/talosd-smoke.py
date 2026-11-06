#!/usr/bin/env python3
"""Deterministic process-level smoke harness for talosd and talosdctl.

The harness launches the real binaries against the scripted ACP fixture in a
temporary directory. It never reads or writes ``~/.talosd``, ``~/.paseo`` or a
real provider home. All child processes receive ``HOME`` and ``TALOSD_HOME``
pointing inside the temporary root, and cleanup runs even after a failed check.

Prerequisites (no cargo invocation happens here):

    cargo build -p talosd -p talosdctl -p talos-acp --bins
    python3 scripts/talosd-smoke.py --verbose

Binary paths can be overridden with ``TALOSD_SMOKE_DAEMON_BIN``,
``TALOSD_SMOKE_CTL_BIN`` and ``TALOSD_ACP_FIXTURE_BIN``.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

TERMINAL_RUN_STATES = {"Succeeded", "Failed", "Cancelled", "Interrupted"}
SCENARIOS = (
    "lifecycle",
    "keyed",
    "isolation",
    "redaction",
    "interrupt",
    "cancel-permission",
    "terminal",
    "projects",
)
START_TIMEOUT = 25.0
STEP_TIMEOUT = 30.0
SHUTDOWN_TIMEOUT = 15.0
POLL_INTERVAL = 0.05
REPO_ROOT = Path(__file__).resolve().parents[1]


class SmokeFailure(RuntimeError):
    """A checked smoke scenario failed."""


def _process_exists(pid: int) -> bool:
    stat = Path(f"/proc/{pid}/stat")
    try:
        # A zombie has terminated and cannot execute, even before init reaps it.
        fields = stat.read_text(encoding="ascii").rsplit(")", 1)[1].strip().split()
        if fields and fields[0] == "Z":
            return False
    except OSError:
        pass
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _kind(event: dict) -> tuple[str, object]:
    kind = event.get("kind")
    if not isinstance(kind, dict) or len(kind) != 1:
        raise SmokeFailure(f"event has an unexpected kind: {event!r}")
    name, payload = next(iter(kind.items()))
    return name, payload


class Daemon:
    def __init__(self, name: str, home: Path, token: str, proc, log_path: Path, log_file):
        self.name = name
        self.home = home
        self.socket = home / "talosd.sock"
        self.token = token
        self.proc = proc
        self.log_path = log_path
        self.log_file = log_file


class Harness:
    def __init__(self, args: argparse.Namespace):
        self.args = args
        # Validate prerequisites before creating the temp root so a missing
        # binary cannot leave an unreferenced directory behind.
        self.talosd = self._binary("TALOSD_SMOKE_DAEMON_BIN", "talosd")
        self.talosdctl = self._binary("TALOSD_SMOKE_CTL_BIN", "talosdctl")
        self.fixture = self._binary("TALOSD_ACP_FIXTURE_BIN", "acp-fixture")
        self.root = Path(tempfile.mkdtemp(prefix="talosd-smoke-")).resolve()
        self.fake_home = self.root / "fake-home"
        self.fake_home.mkdir(parents=True)
        self.traces = self.root / "traces"
        self.traces.mkdir()
        self.work = self.root / "work"
        self.work.mkdir()
        self.tmp = self.root / "tmp"
        self.tmp.mkdir()
        self.runtime = self.root / "runtime"
        self.runtime.mkdir()
        self.daemons: list[Daemon] = []
        self.secret = "smoke-secret-do-not-leak"
        self.verbose = args.verbose

    def _binary(self, env_name: str, name: str) -> Path:
        configured = os.environ.get(env_name)
        path = Path(configured) if configured else REPO_ROOT / "target/debug" / name
        if not path.is_absolute():
            path = REPO_ROOT / path
        path = path.resolve()
        if not path.is_file():
            raise SmokeFailure(
                f"missing {name} binary at {path}; build it first with:\n"
                "  cargo build -p talosd -p talosdctl -p talos-acp --bins\n"
                f"or set {env_name}"
            )
        return path

    def log(self, message: str) -> None:
        if self.verbose:
            print(f"[smoke] {message}", flush=True)

    def check(self, condition: bool, message: str) -> None:
        if not condition:
            raise SmokeFailure(message)

    def wait_until(self, description: str, probe, timeout: float = STEP_TIMEOUT):
        deadline = time.monotonic() + timeout
        last_error: object = None
        while time.monotonic() < deadline:
            try:
                value = probe()
            except SmokeFailure:
                raise
            except Exception as error:  # transient CLI/socket failures while starting
                last_error = error
                value = None
            if value:
                return value
            time.sleep(POLL_INTERVAL)
        raise SmokeFailure(f"timed out waiting for {description}; last error: {last_error!r}")

    # ------------------------------------------------------------------ process

    def child_env(self, home: Path) -> dict[str, str]:
        env = os.environ.copy()
        for key in list(env):
            if key.startswith("TALOSD_") or key.startswith("TALOSD_ACP_FIXTURE_"):
                env.pop(key)
        env.pop("FIXTURE_SECRET", None)
        env.pop("TALOSD_DEFAULT_PROVIDER", None)
        env["HOME"] = str(self.fake_home)
        env["TALOSD_HOME"] = str(home)
        env["TMPDIR"] = str(self.tmp)
        env["XDG_CONFIG_HOME"] = str(self.fake_home / ".config")
        env["XDG_DATA_HOME"] = str(self.fake_home / ".local/share")
        env["XDG_CACHE_HOME"] = str(self.fake_home / ".cache")
        env["XDG_RUNTIME_DIR"] = str(self.runtime)
        # Core/ACP warnings are the primary signal for lifecycle races: a stale
        # sequence cache or an unpersisted terminal state otherwise looks like a
        # silent timeout. Keep the default narrow but include those targets.
        env["RUST_LOG"] = env.get(
            "RUST_LOG", "talosd=debug,talos_core=debug,talos_acp=debug"
        )
        env["NO_COLOR"] = "1"
        return env

    def write_config(
        self,
        name: str,
        home: Path,
        *,
        event_capacity: int,
        mode: str = "scripted",
        echo_secret: bool = False,
    ) -> None:
        trace = self.traces / f"{name}.jsonl"
        env = {
            "TALOSD_ACP_FIXTURE_MODE": mode,
            "TALOSD_ACP_FIXTURE_TRACE": str(trace),
            "FIXTURE_SECRET": self.secret,
        }
        if echo_secret:
            env["TALOSD_ACP_FIXTURE_ECHO_SECRET"] = "1"
        providers = [
            {
                "id": "talos",
                "name": "Talos Fixture",
                "command": str(self.fixture),
                "env": env,
            },
            {
                "id": "claude",
                "name": "Claude Code Fixture",
                "command": str(home / "missing-acp-binary"),
                "env": {},
            },
        ]
        lines = [
            "version = 1",
            'default_provider = "talos"',
            f"event_capacity = {event_capacity}",
            "shutdown_grace_ms = 1500",
            "",
        ]
        for provider in providers:
            rendered_env = ", ".join(
                f"{json.dumps(key)} = {json.dumps(value)}"
                for key, value in provider["env"].items()
            )
            lines.extend(
                [
                    "[[providers]]",
                    f"id = {json.dumps(provider['id'])}",
                    f"display_name = {json.dumps(provider['name'])}",
                    'kind = "acp"',
                    "enabled = true",
                    f"command = {json.dumps(provider['command'])}",
                    "args = []",
                    f"env = {{ {rendered_env} }}",
                    f"cwd = {json.dumps(str(self.work))}",
                    "",
                ]
            )
        config = home / "config.toml"
        config.write_text("\n".join(lines), encoding="utf-8")
        config.chmod(0o600)

    def start_daemon(
        self,
        name: str,
        *,
        event_capacity: int = 64,
        echo_secret: bool = False,
    ) -> Daemon:
        home = self.root / "homes" / name
        home.mkdir(parents=True, exist_ok=True)
        self.write_config(
            name,
            home,
            event_capacity=event_capacity,
            echo_secret=echo_secret,
        )
        token = f"token-{name}-{time.time_ns()}"
        token_path = home / "auth.token"
        token_path.write_text(token + "\n", encoding="utf-8")
        token_path.chmod(0o600)
        log_path = self.traces / f"{name}-talosd.log"
        log_file = log_path.open("w", encoding="utf-8")
        try:
            proc = subprocess.Popen(  # noqa: S603 - all paths are harness-owned
                [str(self.talosd)],
                cwd=self.work,
                env=self.child_env(home),
                stdout=log_file,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        except OSError as error:
            log_file.close()
            raise SmokeFailure(
                f"cannot launch talosd at {self.talosd}: {error}"
            ) from error
        daemon = Daemon(name, home, token, proc, log_path, log_file)
        self.daemons.append(daemon)
        self.log(f"starting daemon {name} pid={proc.pid} home={home}")
        try:
            self.wait_until(
                f"daemon {name} health",
                lambda: self._health_probe(daemon),
                START_TIMEOUT,
            )
        except SmokeFailure:
            self.dump_log(daemon)
            raise
        self.check(daemon.socket.exists(), f"daemon {name} did not create its socket")
        return daemon

    def _health_probe(self, daemon: Daemon) -> bool:
        if daemon.proc.poll() is not None:
            raise SmokeFailure(
                f"daemon {daemon.name} exited during startup with {daemon.proc.returncode}"
            )
        result = self.run_ctl(daemon, ["health"], timeout=5)
        return result.returncode == 0

    def dump_log(self, daemon: Daemon) -> None:
        try:
            daemon.log_file.flush()
            text = daemon.log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            return
        if text:
            print(f"--- {daemon.name} daemon log ---", file=sys.stderr)
            print("\n".join(text.splitlines()[-80:]), file=sys.stderr)

    def daemon_diagnostics(self, daemon: Daemon, limit: int = 10) -> str:
        """Return recent daemon warnings/errors to attach to a failure message."""
        try:
            daemon.log_file.flush()
            text = daemon.log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            return ""
        lines = [
            line
            for line in text.splitlines()
            if '"level":"WARN"' in line or '"level":"ERROR"' in line
        ]
        if not lines:
            return ""
        return "\nrecent daemon warnings/errors:\n" + "\n".join(lines[-limit:])

    @staticmethod
    def describe_events(events: list[dict], limit: int = 8) -> str:
        """Compact event sequence/run-id summary for attribution failures."""
        if not events:
            return "[]"
        rendered = []
        for event in events[:limit]:
            name, payload = _kind(event)
            detail = json.dumps(payload, sort_keys=True, separators=(",", ":"))
            rendered.append(
                f"seq={event.get('sequence')} "
                f"run={event.get('run_id') or '<none>'} "
                f"{name} {detail[:200]}"
            )
        if len(events) > limit:
            rendered.append(f"... {len(events) - limit} more")
        return "; ".join(rendered)

    def stop_daemon(self, daemon: Daemon, *, graceful: bool = True) -> None:
        if daemon.proc.poll() is None and graceful:
            try:
                self.ctl(daemon, "shutdown")
            except SmokeFailure:
                pass
            try:
                daemon.proc.wait(timeout=SHUTDOWN_TIMEOUT)
            except subprocess.TimeoutExpired:
                self.log(f"daemon {daemon.name} did not stop; killing process group")
        if daemon.proc.poll() is None:
            self._kill_group(daemon.proc)
        if daemon.proc.poll() is None:
            try:
                daemon.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
        try:
            daemon.log_file.close()
        except OSError:
            pass
        if daemon in self.daemons:
            self.daemons.remove(daemon)

    @staticmethod
    def _kill_group(proc) -> None:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            try:
                proc.kill()
            except ProcessLookupError:
                pass

    # ---------------------------------------------------------------------- CLI

    def run_ctl(self, daemon: Daemon, args: list[str], *, timeout: float = STEP_TIMEOUT):
        return self.run_ctl_with(
            daemon.home, daemon.socket, args, token=None, timeout=timeout
        )

    def run_ctl_with(
        self,
        home: Path,
        socket_path: Path,
        args: list[str],
        *,
        token: str | None,
        timeout: float = STEP_TIMEOUT,
    ):
        command = [
            str(self.talosdctl),
            "--json",
            "--home",
            str(home),
            "--socket",
            str(socket_path),
        ]
        if token is not None:
            command.extend(["--auth-token", token])
        command.extend(args)
        try:
            return subprocess.run(  # noqa: S603 - harness-owned binaries
                command,
                cwd=self.work,
                env=self.child_env(home),
                capture_output=True,
                text=True,
                timeout=timeout,
                check=False,
            )
        except subprocess.TimeoutExpired as error:
            raise SmokeFailure(
                f"talosdctl timed out after {timeout:.0f}s: {' '.join(args)}"
            ) from error
        except FileNotFoundError as error:
            raise SmokeFailure(
                f"talosdctl disappeared during the smoke run ({self.talosdctl}); "
                "do not rebuild while the harness is running"
            ) from error

    def ctl(self, daemon: Daemon, *args: str, timeout: float = STEP_TIMEOUT):
        result = self.run_ctl(daemon, list(args), timeout=timeout)
        if result.returncode != 0:
            raise SmokeFailure(
                f"talosdctl {' '.join(args)} failed ({result.returncode})\n"
                f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            )
        try:
            return json.loads(result.stdout)
        except json.JSONDecodeError as error:
            raise SmokeFailure(
                f"talosdctl {' '.join(args)} returned invalid JSON: {error}\n"
                f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            ) from error

    def ctl_error(self, daemon: Daemon, *args: str):
        result = self.run_ctl(daemon, list(args))
        self.check(
            result.returncode != 0,
            f"talosdctl {' '.join(args)} unexpectedly succeeded: {result.stdout}",
        )
        return result

    # -------------------------------------------------------------------- events

    def event_page(self, daemon: Daemon, *, cursors: dict[str, int] | None = None,
                   limit: int = 10000) -> dict:
        """Collect lossless multi-session replay pages into one ordered view."""
        current = dict(cursors or {})
        collected: list[dict] = []
        session_cursors: dict[str, int] = {}
        while True:
            page = self.ctl(
                daemon,
                "events",
                "replay",
                "--cursors",
                json.dumps(current, separators=(",", ":")),
                "--limit",
                str(limit),
            )
            collected.extend(page.get("events") or [])
            session_cursors = page.get("session_cursors") or {}
            if page.get("has_more") is not True:
                break
            if session_cursors == current:
                raise SmokeFailure("lossless replay did not advance its cursors")
            current = session_cursors
        collected.sort(
            key=lambda event: (
                event.get("timestamp"),
                event.get("session_id"),
                event.get("sequence"),
            )
        )
        return {
            "events": collected,
            "session_cursors": session_cursors,
            "cursor_scope": "per_session_sequence",
            "has_more": False,
        }

    def all_events(self, daemon: Daemon) -> list[dict]:
        return self.event_page(daemon)["events"]

    def session_events(self, daemon: Daemon, session_id: str) -> list[dict]:
        page = self.ctl(
            daemon,
            "session",
            "replay",
            "--session",
            session_id,
            "--after",
            "0",
            "--limit",
            "10000",
        )
        return page["events"]

    def tool_events(
        self, daemon: Daemon, session_id: str, tool_id: str
    ) -> list[tuple[int, dict]]:
        result = []
        for event in self.session_events(daemon, session_id):
            name, payload = _kind(event)
            if name == "Tool" and isinstance(payload, dict):
                if payload.get("id") == tool_id:
                    result.append((event["sequence"], payload))
        return result

    def wait_event(self, daemon: Daemon, predicate, description: str, timeout=STEP_TIMEOUT):
        def probe():
            for event in self.all_events(daemon):
                if predicate(event):
                    return event
            return None

        return self.wait_until(description, probe, timeout)

    def start_prompt(self, daemon: Daemon, session_id: str, text: str) -> str:
        result = self.ctl(daemon, "session", "prompt", session_id, text)
        self.check(
            result.get("status") == "running",
            f"prompt did not start a run: {result}",
        )
        return str(result["run_id"])

    def keyed_prompt(
        self, daemon: Daemon, session_id: str, text: str, request_key: str
    ) -> dict:
        return self.ctl(
            daemon,
            "session",
            "prompt",
            session_id,
            text,
            "--request-key",
            request_key,
        )

    def wait_run_terminal(self, daemon: Daemon, session_id: str, run_id: str) -> str:
        def probe():
            for event in self.session_events(daemon, session_id):
                if str(event.get("run_id")) != str(run_id):
                    continue
                name, payload = _kind(event)
                if name == "RunState" and isinstance(payload, dict):
                    state = payload.get("state")
                    if state in TERMINAL_RUN_STATES:
                        return state
            return None

        try:
            return self.wait_until(f"run {run_id} terminal state", probe, STEP_TIMEOUT)
        except SmokeFailure as error:
            states = []
            try:
                for event in self.session_events(daemon, session_id):
                    if str(event.get("run_id")) != str(run_id):
                        continue
                    name, payload = _kind(event)
                    if name == "RunState" and isinstance(payload, dict):
                        states.append(payload.get("state"))
            except Exception:  # diagnostics only
                pass
            raise SmokeFailure(
                f"{error}; durable run states for {run_id}: {states}"
                f"{self.daemon_diagnostics(daemon)}"
            ) from error

    def wait_pending(self, daemon: Daemon, session_id: str) -> list[dict]:
        def probe():
            pending = self.ctl(daemon, "permission", "list")
            matches = [
                item for item in pending if item.get("session_id") == session_id
            ]
            return matches or None

        return self.wait_until(
            f"pending permission for {session_id}", probe, STEP_TIMEOUT
        )

    def wait_no_pending(self, daemon: Daemon, session_id: str) -> None:
        def probe():
            pending = self.ctl(daemon, "permission", "list")
            matches = [
                item for item in pending if item.get("session_id") == session_id
            ]
            return True if not matches else None

        try:
            self.wait_until(
                f"permission resolution for {session_id}", probe, STEP_TIMEOUT
            )
        except SmokeFailure as error:
            pending = self.ctl(daemon, "permission", "list")
            raise SmokeFailure(
                f"{error}; pending permissions: {pending}"
            ) from error

    def permission_outcome(
        self, daemon: Daemon, session_id: str, permission_id: str
    ) -> str | None:
        requested_sequence: int | None = None
        for event in self.session_events(daemon, session_id):
            name, payload = _kind(event)
            if name == "PermissionRequested" and isinstance(payload, dict):
                if str(payload.get("id")) == permission_id:
                    requested_sequence = event["sequence"]
                continue
            if (
                name == "PermissionResolved"
                and requested_sequence is not None
                and event["sequence"] > requested_sequence
            ):
                if isinstance(payload, str):
                    return payload
                if isinstance(payload, dict) and payload:
                    return next(iter(payload))
                return None
        return None

    def wait_permission_outcome(
        self,
        daemon: Daemon,
        session_id: str,
        permission_id: str,
        expected: str,
    ) -> None:
        def probe():
            outcome = self.permission_outcome(daemon, session_id, permission_id)
            if outcome is not None and outcome != expected:
                raise SmokeFailure(
                    f"permission {permission_id} resolved as {outcome}, expected {expected}"
                )
            return outcome

        self.wait_until(
            f"permission {permission_id} outcome {expected}", probe, STEP_TIMEOUT
        )

    # ----------------------------------------------------------------- scenarios

    def run(self) -> None:
        scenarios = {
            "lifecycle": self.scenario_lifecycle,
            "keyed": self.scenario_keyed_prompts,
            "isolation": self.scenario_isolation_auth_and_protocol,
            "redaction": self.scenario_redaction,
            "interrupt": self.scenario_interrupt_and_restart,
            "cancel-permission": self.scenario_permission_cancel,
            "terminal": self.scenario_terminal,
            "projects": self.scenario_projects,
        }
        selected = self.args.scenario or list(SCENARIOS)
        for name in selected:
            self.log(f"scenario {name}")
            scenarios[name]()
        self.log("all selected smoke scenarios passed")

    def scenario_terminal(self) -> None:
        daemon = self.start_daemon("terminal")
        hard_pid: int | None = None
        hard_daemon: Daemon | None = None
        try:
            workspace_path = self.work / "terminal-workspace"
            other_path = self.work / "other-terminal-workspace"
            workspace_path.mkdir()
            other_path.mkdir()
            workspace = self.ctl(daemon, "workspace", "add", "Terminal workspace", str(workspace_path))
            other = self.ctl(daemon, "workspace", "add", "Other workspace", str(other_path))
            process = "printf READY; read line; printf 'GOT:%s' \"$line\""
            terminal = self.ctl(
                daemon, "terminal", "create", workspace["id"], "--program", "sh",
                "--arg=-c", f"--arg={process}",
            )
            terminal_id = terminal["id"]
            self.check(
                any(item.get("id") == terminal_id for item in self.ctl(daemon, "terminal", "list", workspace["id"])),
                "created terminal is not listed for its workspace",
            )
            self.check(
                terminal_id not in {item.get("id") for item in self.ctl(daemon, "terminal", "list", other["id"])},
                "terminal leaked into another workspace listing",
            )
            self.wait_until(
                "PTY initial output",
                lambda: "READY" in self.ctl(daemon, "terminal", "capture", workspace["id"], terminal_id).get("data", ""),
                STEP_TIMEOUT,
            )
            rejected = self.run_ctl(daemon, ["terminal", "input", other["id"], terminal_id, "wrong\n"])
            self.check(rejected.returncode != 0, "cross-workspace terminal input was accepted")
            self.ctl(daemon, "terminal", "resize", workspace["id"], terminal_id, "40", "100")
            self.ctl(daemon, "terminal", "input", workspace["id"], terminal_id, "from-cli\n")
            self.wait_until(
                "PTY CLI input output",
                lambda: "GOT:from-cli" in self.ctl(daemon, "terminal", "capture", workspace["id"], terminal_id).get("data", ""),
                STEP_TIMEOUT,
            )
            self.ctl(daemon, "terminal", "kill", workspace["id"], terminal_id)
            self.check(
                all(item.get("id") != terminal_id for item in self.ctl(daemon, "terminal", "list", workspace["id"])),
                "killed terminal remained listed",
            )

            long_lived = self.ctl(
                daemon, "terminal", "create", workspace["id"], "--program", "sh",
                "--arg=-c", "--arg=sleep 30 & echo $!; wait",
            )
            long_id = long_lived["id"]

            def read_descendant_pid():
                data = self.ctl(daemon, "terminal", "capture", workspace["id"], long_id).get("data", "")
                return next((int(line.strip()) for line in data.splitlines() if line.strip().isdigit()), None)

            child_pid = self.wait_until("PTY descendant PID", read_descendant_pid, STEP_TIMEOUT)
            self.stop_daemon(daemon)
            self.wait_until(
                f"PTY descendant {child_pid} cleanup after daemon shutdown",
                lambda: not _process_exists(child_pid),
                5.0,
            )

            hard_daemon = self.start_daemon("terminal-hard-crash")
            hard_workspace = self.ctl(hard_daemon, "workspace", "add", "Hard crash workspace", str(workspace_path))
            hard_crash = self.ctl(
                hard_daemon, "terminal", "create", hard_workspace["id"], "--program", "sh",
                "--arg=-c", "--arg=sleep 30 & echo $!; wait",
            )
            def read_hard_pid() -> int | None:
                nonlocal hard_pid
                data = self.ctl(hard_daemon, "terminal", "capture", hard_workspace["id"], hard_crash["id"]).get("data", "")
                hard_pid = next((int(line.strip()) for line in data.splitlines() if line.strip().isdigit()), hard_pid)
                return hard_pid

            hard_pid = self.wait_until(
                "PTY descendant before hard daemon crash", read_hard_pid, STEP_TIMEOUT
            )
            hard_daemon.proc.kill()
            hard_daemon.proc.wait(timeout=5)
            self.stop_daemon(hard_daemon, graceful=False)
            self.wait_until(
                f"PTY descendant {hard_pid} cleanup after hard daemon crash",
                lambda: not _process_exists(hard_pid),
                5.0,
            )
        finally:
            if daemon.proc.poll() is None:
                self.stop_daemon(daemon)
            if hard_daemon is not None and hard_daemon.proc.poll() is None:
                self.stop_daemon(hard_daemon, graceful=False)
            if hard_pid is not None and _process_exists(hard_pid):
                try:
                    os.kill(hard_pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

    def scenario_projects(self) -> None:
        daemon = self.start_daemon("projects")
        try:
            root = self.work / "project-root"
            workspace_path = root / "workspace"
            outside_path = self.work / "outside-project"
            workspace_path.mkdir(parents=True)
            outside_path.mkdir()
            subprocess.run(["git", "init", "--quiet", "-b", "main"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.name", "Talos Smoke"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "talos-smoke@example.invalid"], cwd=root, check=True)
            (root / "tracked.txt").write_text("fixture\\n", encoding="utf-8")
            subprocess.run(["git", "add", "tracked.txt"], cwd=root, check=True)
            subprocess.run(["git", "commit", "--quiet", "-m", "initial"], cwd=root, check=True)
            project = self.ctl(daemon, "project", "create", "Smoke project", str(root))
            self.check(project.get("name") == "Smoke project", f"project create: {project}")
            listed = self.ctl(daemon, "project", "list")
            self.check(any(item.get("id") == project.get("id") for item in listed), "project missing from list")
            self.ctl(daemon, "project", "rename", project["id"], "Renamed project")
            worktree = self.ctl(daemon, "worktree", "create", project["id"], "Smoke worktree", "feature/smoke", "HEAD")
            worktree_path = Path(worktree["path"])
            self.check(worktree_path.is_dir(), "managed worktree path was not created")
            (worktree_path / "untracked.txt").write_text("preserve me", encoding="utf-8")
            dirty_remove = self.run_ctl(daemon, ["worktree", "delete", worktree["id"]])
            self.check(dirty_remove.returncode != 0, "dirty managed worktree was removed")
            self.check((worktree_path / "untracked.txt").read_text(encoding="utf-8") == "preserve me", "dirty worktree data was lost")
            (worktree_path / "untracked.txt").unlink()
            self.ctl(daemon, "worktree", "delete", worktree["id"])
            self.check(not worktree_path.exists(), "managed worktree path survived delete")
            missing_restore = self.run_ctl(daemon, ["workspace", "restore", worktree["id"]])
            self.check(missing_restore.returncode != 0, "deleted managed worktree was restored without a checkout")
            workspace = self.ctl(daemon, "workspace", "add", "Inside", str(workspace_path))
            outside = self.ctl(daemon, "workspace", "add", "Outside", str(outside_path))
            self.ctl(daemon, "workspace", "set-project", workspace["id"], project["id"])
            rejected = self.run_ctl(daemon, ["workspace", "set-project", outside["id"], project["id"]])
            self.check(rejected.returncode != 0, "project association accepted a workspace outside its root")
            rejected_delete = self.run_ctl(daemon, ["project", "delete", project["id"]])
            self.check(rejected_delete.returncode != 0, "project with associated workspace was deleted")
            self.ctl(daemon, "workspace", "set-project", workspace["id"], "none")
            retained_delete = self.run_ctl(daemon, ["project", "delete", project["id"]])
            self.check(
                retained_delete.returncode != 0,
                "project with retained managed-worktree ownership was deleted",
            )
            standalone = self.ctl(
                daemon,
                "project",
                "create",
                "Standalone smoke project",
                str(outside_path),
            )
            self.ctl(daemon, "project", "delete", standalone["id"])
            remaining = self.ctl(daemon, "project", "list")
            self.check(
                [item.get("id") for item in remaining] == [project["id"]],
                "unlinked project was not deleted or owned project association was lost",
            )
        finally:
            if daemon.proc.poll() is None:
                self.stop_daemon(daemon)

    def scenario_lifecycle(self) -> None:
        daemon = self.start_daemon("main", event_capacity=1)

        health = self.ctl(daemon, "health")
        self.check(health.get("status") == "ok", f"health is not ok: {health}")

        providers = self.ctl(daemon, "provider", "list")
        self.check(
            {item.get("id") for item in providers} >= {"talos", "claude"},
            f"provider list missing fixture providers: {providers}",
        )
        diagnostic = self.ctl(daemon, "provider", "diagnostic", "talos")
        self.check(diagnostic.get("available") is True, f"fixture unavailable: {diagnostic}")
        self.check(
            self.secret not in json.dumps(diagnostic),
            "provider diagnostic leaked a configured environment value",
        )
        broken = self.ctl(daemon, "provider", "diagnostic", "claude")
        self.check(
            broken.get("available") is False,
            f"missing provider executable reported available: {broken}",
        )

        workspace_path = self.work / "workspace-alpha"
        workspace_path.mkdir()
        workspace = self.ctl(
            daemon, "workspace", "add", "Alpha Smoke", str(workspace_path)
        )
        self.check(workspace.get("name") == "Alpha Smoke", f"workspace: {workspace}")
        listed = self.ctl(daemon, "workspace", "list")
        self.check(
            any(item.get("id") == workspace["id"] for item in listed),
            "created workspace is not listed",
        )
        shown = self.ctl(daemon, "workspace", "show", workspace["id"])
        self.check(shown.get("id") == workspace["id"], f"workspace show: {shown}")

        created = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Smoke Session",
        )
        agent = created["agent"]
        session = created["session"]
        session_id = session["id"]
        self.check(agent.get("title") == "Smoke Session", f"agent: {agent}")
        self.check(agent.get("provider") == "talos", f"agent provider: {agent}")
        self.check(
            session.get("provider_session_id") == "fixture-session",
            f"native session handle: {session}",
        )

        run_id = self.start_prompt(daemon, session_id, "hello smoke")
        state = self.wait_run_terminal(daemon, session_id, run_id)
        self.check(state == "Succeeded", f"first prompt ended as {state}")
        usage = self.ctl(daemon, "usage")
        self.check(usage["totals"]["input_tokens"] == 2, f"known input usage was lost: {usage}")
        self.check(usage["totals"]["output_tokens"] == 1, f"known output usage was lost: {usage}")
        self.check(usage["totals"]["cost_micros"] is None, f"unknown cost was fabricated: {usage}")
        scoped_usage = self.ctl(daemon, "usage", "--session", session_id)
        self.check(scoped_usage["totals"] == usage["totals"], "session-scoped usage differs from global usage")
        events = self.session_events(daemon, session_id)
        assistant = [
            payload["text"]
            for event in events
            for name, payload in [_kind(event)]
            if name == "AssistantText"
        ]
        self.check(
            "hello " in assistant and "world" in assistant,
            f"assistant text missing: {assistant}",
        )
        usage = [
            payload
            for event in events
            for name, payload in [_kind(event)]
            if name == "Usage"
        ]
        self.check(
            any(
                item.get("input_tokens") == 2 and item.get("output_tokens") == 1
                for item in usage
            ),
            f"usage event missing: {usage}",
        )

        follow_up = self.start_prompt(daemon, session_id, "follow up")
        self.check(
            self.wait_run_terminal(daemon, session_id, follow_up) == "Succeeded",
            "follow-up prompt did not succeed",
        )
        user_texts = [
            payload["text"]
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if name == "UserText"
        ]
        self.check(
            user_texts == ["hello smoke", "follow up"],
            f"durable user text replay: {user_texts}",
        )

        tool_run = self.start_prompt(daemon, session_id, "tool lifecycle")
        self.check(
            self.wait_run_terminal(daemon, session_id, tool_run) == "Succeeded",
            "tool lifecycle prompt did not succeed",
        )
        tools = self.tool_events(daemon, session_id, "fixture-tool-1")
        self.check(len(tools) == 2, f"expected two durable tool events: {tools}")
        self.check(
            [payload.get("status") for _, payload in tools]
            == ["Running", "Completed"],
            f"tool status lifecycle: {[p.get('status') for _, p in tools]}",
        )
        self.check(
            tools[0][0] < tools[1][0],
            "tool lifecycle events are not ordered",
        )
        self.check(
            tools[0][1].get("input") == {"command": "echo tool"},
            f"tool input was not replayed: {tools[0][1]}",
        )
        self.check(
            tools[1][1].get("output") == {"stdout": "tool ok"},
            f"tool output was not replayed: {tools[1][1]}",
        )
        tool_lifecycle_events = [
            event
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if (name == "Tool" and isinstance(payload, dict)
                and payload.get("id") == "fixture-tool-1")
            or (name == "AssistantText" and payload.get("text") == "tool complete")
        ]
        misattributed = [
            event
            for event in tool_lifecycle_events
            if str(event.get("run_id")) != tool_run
        ]
        observed_runs = sorted(
            {
                str(event.get("run_id"))
                for event in self.session_events(daemon, session_id)
            }
        )
        self.check(
            tool_lifecycle_events and not misattributed,
            "tool lifecycle events were not attributed to the tool prompt run "
            f"{tool_run}; offending: {self.describe_events(misattributed)}; "
            f"observed run ids: {observed_runs}"
            f"{self.daemon_diagnostics(daemon)}",
        )
        tool_usage = [
            event
            for event in self.session_events(daemon, session_id)
            for name, _payload in [_kind(event)]
            if name == "Usage" and str(event.get("run_id")) == tool_run
        ]
        self.check(
            tool_usage,
            "tool prompt usage was not attributed to its run "
            f"{tool_run}{self.daemon_diagnostics(daemon)}",
        )
        global_tools = [
            (event["session_id"], event["sequence"])
            for event in self.all_events(daemon)
            for name, payload in [_kind(event)]
            if name == "Tool"
            and isinstance(payload, dict)
            and payload.get("id") == "fixture-tool-1"
        ]
        self.check(
            len(global_tools) == 2,
            f"aggregate replay duplicated tool events: {global_tools}",
        )

        # Start a follow-up immediately after the tool terminal state. Any late
        # provider update must stay attributed to the tool run, never the new run.
        rapid_run = self.start_prompt(daemon, session_id, "rapid followup")
        self.check(
            self.wait_run_terminal(daemon, session_id, rapid_run) == "Succeeded",
            "rapid follow-up after tool prompt did not succeed",
        )
        current_tools = self.tool_events(daemon, session_id, "fixture-tool-1")
        tool_snapshot = [
            event
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if name == "Tool"
            and isinstance(payload, dict)
            and payload.get("id") == "fixture-tool-1"
        ]
        self.check(
            current_tools == tools,
            "rapid follow-up duplicated or changed tool events: "
            f"{self.describe_events(tool_snapshot)}",
        )
        followup_tools = [
            event
            for event in self.session_events(daemon, session_id)
            for name, _payload in [_kind(event)]
            if name == "Tool" and str(event.get("run_id")) == rapid_run
        ]
        self.check(
            not followup_tools,
            "late tool updates were attributed to the follow-up run "
            f"{rapid_run}: {self.describe_events(followup_tools)}"
            f"{self.daemon_diagnostics(daemon)}",
        )
        late_assistant = [
            event
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if name == "AssistantText"
            and payload.get("text") == "tool complete"
            and str(event.get("run_id")) == rapid_run
        ]
        self.check(
            not late_assistant,
            "late assistant tool output was attributed to the follow-up run "
            f"{rapid_run}: {self.describe_events(late_assistant)}"
            f"{self.daemon_diagnostics(daemon)}",
        )

        renamed = self.ctl(daemon, "session", "rename", session_id, "Renamed Smoke")
        self.check(renamed.get("title") == "Renamed Smoke", f"rename: {renamed}")
        shown_session = self.ctl(daemon, "session", "show", session_id)
        self.check(
            shown_session["session"]["id"] == session_id,
            "session show returned the wrong session",
        )

        allow_run = self.start_prompt(daemon, session_id, "permission allow")
        pending = self.wait_pending(daemon, session_id)
        self.check(len(pending) == 1, f"expected one pending permission: {pending}")
        allow_id = pending[0]["id"]
        detail_options = self._permission_options(daemon, session_id, allow_id)
        self.check(
            {"allow_once", "reject_once"} <= detail_options,
            f"permission options not durable: {detail_options}",
        )
        self.ctl(daemon, "permission", "allow", allow_id)
        self.check(
            self.wait_run_terminal(daemon, session_id, allow_run) == "Succeeded",
            "allow prompt did not succeed",
        )
        self.wait_permission_outcome(daemon, session_id, allow_id, "Allowed")
        duplicate = self.ctl_error(daemon, "permission", "allow", allow_id)
        self.check(
            "not pending" in duplicate.stderr,
            f"duplicate decision was not rejected: {duplicate.stderr}",
        )

        deny_run = self.start_prompt(daemon, session_id, "permission deny")
        pending = self.wait_pending(daemon, session_id)
        deny_id = pending[0]["id"]
        self.ctl(daemon, "permission", "deny", deny_id)
        self.check(
            self.wait_run_terminal(daemon, session_id, deny_run) == "Succeeded",
            "deny prompt did not succeed",
        )
        self.wait_permission_outcome(daemon, session_id, deny_id, "Denied")

        second = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Second Smoke",
        )
        second_id = second["session"]["id"]

        first_run = self.start_prompt(daemon, session_id, "permission alpha")
        second_run = self.start_prompt(daemon, second_id, "permission beta")
        first_pending = self.wait_pending(daemon, session_id)
        second_pending = self.wait_pending(daemon, second_id)
        first_permission = first_pending[0]["id"]
        second_permission = second_pending[0]["id"]
        self.check(
            first_permission != second_permission,
            "two sessions received the same permission id",
        )
        self.ctl(daemon, "permission", "deny", first_permission)
        still_pending = self.wait_pending(daemon, second_id)
        self.check(
            [item["id"] for item in still_pending] == [second_permission],
            "denying one agent changed the other agent's pending permission",
        )
        self.ctl(daemon, "permission", "allow", second_permission)
        self.check(
            self.wait_run_terminal(daemon, session_id, first_run) == "Succeeded",
            "first concurrent run did not finish",
        )
        self.check(
            self.wait_run_terminal(daemon, second_id, second_run) == "Succeeded",
            "second concurrent run did not finish",
        )
        self.wait_permission_outcome(daemon, session_id, first_permission, "Denied")
        self.wait_permission_outcome(daemon, second_id, second_permission, "Allowed")

        cancel_run = self.start_prompt(daemon, session_id, "cancel this prompt")
        self.wait_event(
            daemon,
            lambda event: _kind(event) == (
                "AssistantText",
                {"text": "partial"},
            )
            and event.get("session_id") == session_id,
            "partial output before cancellation",
        )
        self.ctl(daemon, "session", "cancel", session_id)
        self.check(
            self.wait_run_terminal(daemon, session_id, cancel_run) == "Cancelled",
            "cancelled prompt did not reach a terminal Cancelled state",
        )

        archived = self.ctl(daemon, "session", "archive", second_id)
        self.check(archived.get("archived") is True, f"archive: {archived}")
        archived_show = self.ctl(daemon, "session", "show", second_id)
        self.check(
            archived_show["session"]["lifecycle"] == "Archived",
            f"archived lifecycle: {archived_show['session']}",
        )
        archived_prompt = self.ctl_error(
            daemon, "session", "prompt", second_id, "hello after archive"
        )
        self.check(
            archived_prompt.returncode != 0,
            "prompting an archived session unexpectedly succeeded",
        )

        burst_ids = []
        for index in range(3):
            burst = self.ctl(
                daemon,
                "session",
                "new",
                workspace["id"],
                "--provider",
                "talos",
                "--title",
                f"Burst {index}",
            )
            burst_id = burst["session"]["id"]
            burst_ids.append(burst_id)
            run = self.start_prompt(daemon, burst_id, f"burst {index}")
            self.check(
                self.wait_run_terminal(daemon, burst_id, run) == "Succeeded",
                f"burst run {index} did not succeed",
            )

        self.check_cursor_contract(daemon)
        self.tail_follow_dedup_limit_one(daemon, [session_id, burst_ids[0]])
        self.disconnect_and_recover(daemon)
        self.stop_daemon(daemon, graceful=True)
        self.check(
            daemon.proc.returncode is not None,
            "daemon did not exit during graceful shutdown",
        )
        self.check(not daemon.socket.exists(), "daemon socket survived shutdown")
        self.check(
            not (daemon.home / "talosd.lock").exists(),
            "daemon instance lock survived shutdown",
        )
        self.check_fixture_cleanup("main")
        self.restart_main(session_id)

    def _permission_options(self, daemon: Daemon, session_id: str, permission_id: str):
        for event in self.session_events(daemon, session_id):
            name, payload = _kind(event)
            if name != "PermissionRequested" or not isinstance(payload, dict):
                continue
            if str(payload.get("id")) != permission_id:
                continue
            details = payload.get("details") or {}
            options = details.get("options") or []
            return {
                option.get("id")
                for option in options
                if isinstance(option, dict)
            }
        return set()

    def check_cursor_contract(self, daemon: Daemon) -> None:
        page = self.event_page(daemon)
        events = page["events"]
        self.check(page.get("cursor_scope") == "per_session_sequence", f"cursor scope: {page}")
        self.check(events, "no durable events to validate")
        keys = [(event["session_id"], event["sequence"]) for event in events]
        self.check(len(keys) == len(set(keys)), "global replay contains duplicate cursors")
        order = [
            (event["timestamp"], event["session_id"], event["sequence"])
            for event in events
        ]
        self.check(order == sorted(order), "global replay is not ordered")
        by_session: dict[str, list[int]] = {}
        for event in events:
            by_session.setdefault(event["session_id"], []).append(event["sequence"])
        for session_id, sequences in by_session.items():
            self.check(
                sequences == sorted(set(sequences)),
                f"session {session_id} replay is not ordered and unique",
            )
            self.check(
                sequences == list(range(1, sequences[-1] + 1)),
                f"session {session_id} replay has a sequence gap: {sequences}",
            )
            per_session = self.session_events(daemon, session_id)
            self.check(
                [event["sequence"] for event in per_session] == sequences,
                f"per-session replay disagrees with the aggregate view for {session_id}",
            )
        observed = {
            session_id: sequences[-1]
            for session_id, sequences in by_session.items()
        }
        self.check(
            page.get("session_cursors") == observed,
            f"session cursors do not match replay: {page.get('session_cursors')} vs {observed}",
        )
        # Multiple sessions routinely share the same local sequence number. The
        # scalar `after` value is therefore a per-session floor, not a global id.
        all_sequences = [event["sequence"] for event in events]
        self.check(
            len(all_sequences) != len(set(all_sequences)),
            "expected overlapping session-local sequence numbers",
        )

        # Page with a deliberately small limit and prove the cursor map is
        # lossless: the union of pages must be exactly the full replay set.
        paged: list[dict] = []
        cursors: dict[str, int] = {}
        for _ in range(10000):
            raw = self.ctl(
                daemon,
                "events",
                "replay",
                "--cursors",
                json.dumps(cursors, separators=(",", ":")),
                "--limit",
                "1",
            )
            batch = raw.get("events") or []
            paged.extend(batch)
            next_cursors = raw.get("session_cursors") or {}
            if raw.get("has_more") is not True:
                break
            if next_cursors == cursors:
                raise SmokeFailure("small-limit replay did not advance its cursors")
            cursors = next_cursors
        paged_keys = [(event["session_id"], event["sequence"]) for event in paged]
        self.check(
            len(paged_keys) == len(set(paged_keys)),
            "paged replay returned duplicate cursors",
        )
        self.check(
            set(paged_keys) == set(keys),
            "paged replay did not cover the full per-session history",
        )

    @staticmethod
    def _json_objects(text: str) -> list[dict]:
        decoder = json.JSONDecoder()
        objects: list[dict] = []
        index = 0
        while True:
            while index < len(text) and text[index].isspace():
                index += 1
            if index >= len(text):
                break
            try:
                value, end = decoder.raw_decode(text, index)
            except json.JSONDecodeError:
                break
            if isinstance(value, dict):
                objects.append(value)
            index = end
        return objects

    def _tail_keys(self, text: str) -> list[tuple[str, int]]:
        keys: list[tuple[str, int]] = []
        for value in self._json_objects(text):
            events = value.get("events")
            if isinstance(events, list):
                for event in events:
                    if isinstance(event, dict):
                        session = event.get("session_id")
                        sequence = event.get("sequence")
                        if isinstance(session, str) and isinstance(sequence, int):
                            keys.append((session, sequence))
            event = value.get("event")
            if isinstance(event, dict):
                payload = event.get("payload")
                if isinstance(payload, dict):
                    session = payload.get("session_id")
                    sequence = payload.get("sequence")
                    if isinstance(session, str) and isinstance(sequence, int):
                        keys.append((session, sequence))
        return keys

    def tail_follow_dedup_limit_one(
        self, daemon: Daemon, session_ids: list[str]
    ) -> None:
        """Tail with --limit 1 while two sessions progress; require lossless dedup."""
        path = self.root / "tail-limit1.jsonl"
        with path.open("w", encoding="utf-8") as sink:
            tail = subprocess.Popen(  # noqa: S603 - harness-owned binary
                [
                    str(self.talosdctl),
                    "--json",
                    "--home",
                    str(daemon.home),
                    "--socket",
                    str(daemon.socket),
                    "events",
                    "tail",
                    "--follow",
                    "--after",
                    "0",
                    "--limit",
                    "1",
                ],
                cwd=self.work,
                env=self.child_env(daemon.home),
                stdout=sink,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
            try:
                runs = [
                    (session_id, self.start_prompt(daemon, session_id, f"tail {index}"))
                    for index, session_id in enumerate(session_ids)
                ]
                for session_id, run in runs:
                    self.check(
                        self.wait_run_terminal(daemon, session_id, run) == "Succeeded",
                        "tail follow prompt did not succeed",
                    )
                durable = self.event_page(daemon)["events"]
                durable_keys = {
                    (event["session_id"], event["sequence"]) for event in durable
                }

                def probe():
                    keys = self._tail_keys(
                        path.read_text(encoding="utf-8", errors="replace")
                    )
                    self.check(
                        len(keys) == len(set(keys)),
                        "events tail --follow --limit 1 delivered a duplicate event",
                    )
                    return True if set(keys) == durable_keys else None

                self.wait_until(
                    "limit-1 tail to replay every durable event", probe, 20
                )
            finally:
                tail.terminate()
                try:
                    tail.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    tail.kill()
                    tail.wait(timeout=5)
        keys = self._tail_keys(path.read_text(encoding="utf-8", errors="replace"))
        self.check(
            len(keys) == len(set(keys)),
            "events tail --follow --limit 1 delivered a duplicate event",
        )
        self.log(f"limit-1 tail delivered {len(keys)} unique events across two sessions")

    def disconnect_and_recover(self, daemon: Daemon) -> None:
        before = self.event_page(daemon)
        tail = subprocess.Popen(  # noqa: S603 - harness-owned binary
            [
                str(self.talosdctl),
                "--json",
                "--home",
                str(daemon.home),
                "--socket",
                str(daemon.socket),
                "events",
                "tail",
                "--follow",
                "--after",
                "0",
            ],
            cwd=self.work,
            env=self.child_env(daemon.home),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            time.sleep(0.5)
        finally:
            tail.terminate()
            try:
                tail.wait(timeout=5)
            except subprocess.TimeoutExpired:
                tail.kill()
                tail.wait(timeout=5)
        replay = self.event_page(daemon)
        self.check(
            replay["events"] == before["events"],
            "replay changed after a disconnected live-stream client",
        )
        self.log(
            f"live tail disconnected; durable replay still has {len(replay['events'])} events"
        )

    def restart_main(self, session_id: str) -> None:
        daemon = self.start_daemon("main", event_capacity=1)
        tool_before = self.tool_events(daemon, session_id, "fixture-tool-1")
        self.check(len(tool_before) == 2, f"tool history before restart: {tool_before}")
        self.check(
            not self.tool_events(daemon, session_id, "replayed-tool-1"),
            "session replay inserted a stale tool event before load",
        )
        trace_before = self.trace("main")
        new_before = sum(1 for entry in trace_before if entry["event"] == "session/new")
        load_before = sum(1 for entry in trace_before if entry["event"] == "session/load")

        # Sessions are deliberately not auto-loaded after a restart.
        not_loaded = self.ctl_error(
            daemon, "session", "prompt", session_id, "resume-probe"
        )
        self.check(
            "not loaded" in not_loaded.stderr,
            f"restart did not require an explicit load: {not_loaded.stderr}",
        )

        loaded = self.ctl(daemon, "session", "load", session_id)
        self.check(loaded.get("id") == session_id, f"session load: {loaded}")
        trace_after_load = self.trace("main")
        self.check(
            sum(1 for entry in trace_after_load if entry["event"] == "session/new")
            == new_before,
            "restart created a duplicate native provider session",
        )
        self.check(
            sum(1 for entry in trace_after_load if entry["event"] == "session/load")
            == load_before + 1,
            "restart did not resume the stored native session",
        )
        tool_after_load = self.tool_events(daemon, session_id, "fixture-tool-1")
        self.check(
            tool_after_load == tool_before,
            "session load duplicated or changed durable tool events",
        )
        self.check(
            not self.tool_events(daemon, session_id, "replayed-tool-1"),
            "session load appended provider-replayed tool events to a non-empty timeline",
        )

        resumed = self.start_prompt(daemon, session_id, "resume-probe")
        self.check(
            self.wait_run_terminal(daemon, session_id, resumed) == "Succeeded",
            "resumed probe did not succeed",
        )
        self.check(
            any(entry["event"] == "resume-ok" for entry in self.trace("main")),
            "fixture did not observe a native session load before the resume probe",
        )
        assistant = [
            payload["text"]
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if name == "AssistantText"
        ]
        self.check(
            "resumed" in assistant,
            f"resumed probe output missing: {assistant}",
        )
        self.check(
            "replayed" not in assistant,
            "session load duplicated replayed provider output into history",
        )

        follow = self.start_prompt(daemon, session_id, "after restart")
        self.check(
            self.wait_run_terminal(daemon, session_id, follow) == "Succeeded",
            "post-restart follow-up failed",
        )
        tool_after_follow = self.tool_events(daemon, session_id, "fixture-tool-1")
        self.check(
            tool_after_follow == tool_before,
            "post-restart follow-up duplicated or changed durable tool events",
        )
        self.check(
            not self.tool_events(daemon, session_id, "replayed-tool-1"),
            "post-restart follow-up appended provider-replayed tool events",
        )
        self.check_cursor_contract(daemon)
        self.stop_daemon(daemon, graceful=True)
        self.check_fixture_cleanup("main")

    def scenario_interrupt_and_restart(self) -> None:
        """Shut down underneath a pending permission and recover on restart."""
        daemon = self.start_daemon("interrupt", event_capacity=8)
        workspace_path = self.work / "workspace-interrupt"
        workspace_path.mkdir()
        workspace = self.ctl(
            daemon, "workspace", "add", "Interrupt", str(workspace_path)
        )
        created = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Interrupt",
        )
        session_id = created["session"]["id"]
        run = self.start_prompt(
            daemon, session_id, "permission interrupted by shutdown"
        )
        self.wait_pending(daemon, session_id)
        self.log("shutting down with a pending permission")
        self.stop_daemon(daemon, graceful=True)
        self.check(
            daemon.proc.returncode is not None,
            "daemon did not exit during an interrupt shutdown",
        )
        self.check(
            not daemon.socket.exists(), "daemon socket survived an interrupt shutdown"
        )
        self.check(
            not (daemon.home / "talosd.lock").exists(),
            "daemon instance lock survived an interrupt shutdown",
        )
        self.check_fixture_cleanup("interrupt")

        restarted = self.start_daemon("interrupt", event_capacity=8)
        state = self.wait_run_terminal(restarted, session_id, run)
        self.check(
            state in TERMINAL_RUN_STATES,
            f"shutdown-interrupted run has no durable terminal state: {state}",
        )
        self.check(
            self.ctl(restarted, "permission", "list") == [],
            "pending permission survived an interrupt restart",
        )
        loaded = self.ctl(restarted, "session", "load", session_id)
        self.check(loaded.get("id") == session_id, f"interrupt load: {loaded}")
        resumed = self.start_prompt(restarted, session_id, "resume-probe")
        self.check(
            self.wait_run_terminal(restarted, session_id, resumed) == "Succeeded",
            "resume after an interrupt restart failed",
        )
        self.stop_daemon(restarted, graceful=True)
        self.check_fixture_cleanup("interrupt")

    def scenario_permission_cancel(self) -> None:
        """Cancel while a permission decision is pending; reject stale decisions."""
        daemon = self.start_daemon("cancel-perm", event_capacity=8)
        workspace_path = self.work / "workspace-cancel-perm"
        workspace_path.mkdir()
        workspace = self.ctl(
            daemon, "workspace", "add", "Cancel Permission", str(workspace_path)
        )
        created = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Cancel Permission",
        )
        session_id = created["session"]["id"]
        run = self.start_prompt(daemon, session_id, "permission cancel wait")
        pending = self.wait_pending(daemon, session_id)
        permission_id = pending[0]["id"]
        self.ctl(daemon, "session", "cancel", session_id)
        self.check(
            self.wait_run_terminal(daemon, session_id, run) == "Cancelled",
            "permission wait cancellation did not reach a terminal state",
        )
        self.wait_no_pending(daemon, session_id)
        self.wait_permission_outcome(daemon, session_id, permission_id, "Cancelled")
        stale = self.ctl_error(daemon, "permission", "allow", permission_id)
        self.check(
            "not pending" in stale.stderr,
            f"post-cancel decision was not rejected: {stale.stderr}",
        )
        self.stop_daemon(daemon, graceful=True)
        self.check_fixture_cleanup("cancel-perm")

    # ---------------------------------------------------------------- isolation

    def scenario_keyed_prompts(self) -> None:
        """Durable request_key retry/conflict/isolation/restart contract."""
        daemon = self.start_daemon("keyed", event_capacity=8)
        workspace_path = self.work / "workspace-keyed"
        workspace_path.mkdir()
        workspace = self.ctl(
            daemon, "workspace", "add", "Keyed", str(workspace_path)
        )
        first = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Keyed A",
        )
        session_a = first["session"]["id"]
        second = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Keyed B",
        )
        session_b = second["session"]["id"]

        accepted = self.keyed_prompt(daemon, session_a, "keyed hello", "smoke-key-1")
        run_a = str(accepted["run_id"])
        self.check(
            accepted.get("request_key") == "smoke-key-1",
            f"keyed prompt response: {accepted}",
        )
        self.check(accepted.get("restored") is False, f"first keyed prompt: {accepted}")
        self.check(
            self.wait_run_terminal(daemon, session_a, run_a) == "Succeeded",
            "first keyed prompt did not succeed",
        )
        baseline_a = self.session_events(daemon, session_a)
        prompts_after_first = self.trace_count("keyed", "prompt")
        self.check(prompts_after_first >= 1, "fixture did not observe the keyed prompt")

        retry = self.keyed_prompt(daemon, session_a, "keyed hello", "smoke-key-1")
        self.check(str(retry["run_id"]) == run_a, f"retry run id: {retry}")
        self.check(retry.get("restored") is True, f"retry not restored: {retry}")
        self.check(
            retry.get("status") == "succeeded",
            f"terminal retry status: {retry}",
        )
        self.check(
            self.session_events(daemon, session_a) == baseline_a,
            "identical keyed retry appended timeline events",
        )

        conflict = self.ctl_error(
            daemon,
            "session",
            "prompt",
            session_a,
            "keyed different",
            "--request-key",
            "smoke-key-1",
        )
        self.check(
            "-32012" in conflict.stderr or "different payload" in conflict.stderr,
            f"keyed conflict was not rejected: {conflict.stderr}",
        )
        self.check(
            self.session_events(daemon, session_a) == baseline_a,
            "keyed conflict appended timeline events",
        )
        self.check(
            self.trace_count("keyed", "prompt") == prompts_after_first,
            "identical retry or conflict executed the provider prompt again",
        )

        accepted_b = self.keyed_prompt(daemon, session_b, "keyed hello", "smoke-key-1")
        run_b = str(accepted_b["run_id"])
        self.check(run_b != run_a, f"key was not session-scoped: {accepted_b}")
        self.check(
            accepted_b.get("restored") is False,
            f"cross-session first prompt: {accepted_b}",
        )
        self.check(
            self.wait_run_terminal(daemon, session_b, run_b) == "Succeeded",
            "cross-session keyed prompt did not succeed",
        )
        b_events = self.session_events(daemon, session_b)
        self.check(
            [
                payload["text"]
                for event in b_events
                for name, payload in [_kind(event)]
                if name == "UserText"
            ]
            == ["keyed hello"],
            "keyed prompt was not independent across sessions",
        )
        self.check(
            self.session_events(daemon, session_a) == baseline_a,
            "cross-session keyed prompt changed session A",
        )
        b_conflict = self.ctl_error(
            daemon,
            "session",
            "prompt",
            session_b,
            "keyed b different",
            "--request-key",
            "smoke-key-1",
        )
        self.check(
            "-32012" in b_conflict.stderr or "different payload" in b_conflict.stderr,
            f"session B keyed conflict was not rejected: {b_conflict.stderr}",
        )
        self.check(
            self.session_events(daemon, session_b) == b_events,
            "session B keyed conflict appended timeline events",
        )

        tool_accepted = self.keyed_prompt(
            daemon, session_a, "tool keyed", "smoke-key-tool"
        )
        tool_run = str(tool_accepted["run_id"])
        self.check(
            self.wait_run_terminal(daemon, session_a, tool_run) == "Succeeded",
            "keyed tool prompt did not succeed",
        )
        tool_before = self.tool_events(daemon, session_a, "fixture-tool-1")
        self.check(len(tool_before) == 2, f"keyed tool lifecycle: {tool_before}")

        busy_accepted = self.keyed_prompt(
            daemon, session_a, "permission keyed busy", "smoke-key-busy"
        )
        busy_run = str(busy_accepted["run_id"])
        pending = self.wait_pending(daemon, session_a)
        self.check(len(pending) == 1, f"keyed pending permission: {pending}")
        busy_retry = self.keyed_prompt(
            daemon, session_a, "permission keyed busy", "smoke-key-busy"
        )
        self.check(str(busy_retry["run_id"]) == busy_run, f"busy retry: {busy_retry}")
        self.check(
            busy_retry.get("restored") is True,
            f"busy retry was not restored: {busy_retry}",
        )
        self.check(
            busy_retry.get("status") == "running",
            f"busy retry status: {busy_retry}",
        )
        busy_conflict = self.ctl_error(
            daemon,
            "session",
            "prompt",
            session_a,
            "different busy text",
            "--request-key",
            "smoke-key-busy",
        )
        self.check(
            "-32012" in busy_conflict.stderr
            or "different payload" in busy_conflict.stderr,
            f"busy keyed conflict was not rejected: {busy_conflict.stderr}",
        )
        pending = self.wait_pending(daemon, session_a)
        self.check(len(pending) == 1, f"busy conflict changed permissions: {pending}")
        self.ctl(daemon, "permission", "allow", pending[0]["id"])
        self.check(
            self.wait_run_terminal(daemon, session_a, busy_run) == "Succeeded",
            "busy keyed run did not finish",
        )
        busy_user_counts = [
            payload["text"]
            for event in self.session_events(daemon, session_a)
            for name, payload in [_kind(event)]
            if name == "UserText"
        ]
        self.check(
            busy_user_counts.count("permission keyed busy") == 1,
            "busy keyed retry duplicated the user prompt",
        )

        before_restart_a = self.session_events(daemon, session_a)
        before_restart_b = self.session_events(daemon, session_b)
        before_restart_tool = self.tool_events(
            daemon, session_a, "fixture-tool-1"
        )
        before_restart_prompts = self.trace_count("keyed", "prompt")
        before_restart_loads = self.trace_count("keyed", "session/load")
        self.stop_daemon(daemon, graceful=True)
        daemon = self.start_daemon("keyed", event_capacity=8)

        unknown = self.ctl_error(
            daemon,
            "session",
            "prompt",
            session_a,
            "unknown key",
            "--request-key",
            "smoke-key-unknown",
        )
        self.check(
            "not loaded" in unknown.stderr,
            f"unknown key on unloaded session: {unknown.stderr}",
        )

        restored_a = self.keyed_prompt(daemon, session_a, "keyed hello", "smoke-key-1")
        self.check(str(restored_a["run_id"]) == run_a, f"restart retry A: {restored_a}")
        self.check(
            restored_a.get("restored") is True,
            f"restart retry A not restored: {restored_a}",
        )
        self.check(
            restored_a.get("status") == "succeeded",
            f"restart retry A status: {restored_a}",
        )
        self.check(
            self.session_events(daemon, session_a) == before_restart_a,
            "restart keyed retry appended timeline events",
        )
        restored_b = self.keyed_prompt(daemon, session_b, "keyed hello", "smoke-key-1")
        self.check(str(restored_b["run_id"]) == run_b, f"restart retry B: {restored_b}")
        self.check(
            restored_b.get("restored") is True,
            f"restart retry B not restored: {restored_b}",
        )
        self.check(
            self.session_events(daemon, session_b) == before_restart_b,
            "restart session B keyed retry appended timeline events",
        )
        restored_tool = self.keyed_prompt(
            daemon, session_a, "tool keyed", "smoke-key-tool"
        )
        self.check(
            str(restored_tool["run_id"]) == tool_run,
            f"restart tool retry: {restored_tool}",
        )
        self.check(
            restored_tool.get("restored") is True,
            f"restart tool retry not restored: {restored_tool}",
        )
        self.check(
            self.tool_events(daemon, session_a, "fixture-tool-1") == before_restart_tool,
            "restart keyed retry duplicated tool events",
        )
        self.check(
            not self.tool_events(daemon, session_a, "replayed-tool-1"),
            "restart keyed retry replayed provider tool events",
        )
        self.check(
            self.trace_count("keyed", "prompt") == before_restart_prompts,
            "restart keyed retries executed provider prompts again",
        )
        self.check(
            self.trace_count("keyed", "session/load") == before_restart_loads,
            "restart keyed retries loaded the provider before explicit session load",
        )

        loaded = self.ctl(daemon, "session", "load", session_a)
        self.check(loaded.get("id") == session_a, f"keyed load: {loaded}")
        self.check(
            self.trace_count("keyed", "session/load") == before_restart_loads + 1,
            "explicit keyed session load did not resume the provider session",
        )
        after_load = [event["id"] for event in self.session_events(daemon, session_a)]
        after = self.keyed_prompt(
            daemon, session_a, "keyed after load", "smoke-key-after-load"
        )
        after_run = str(after["run_id"])
        self.check(after.get("restored") is False, f"post-load prompt: {after}")
        self.check(
            self.wait_run_terminal(daemon, session_a, after_run) == "Succeeded",
            "post-load keyed prompt did not succeed",
        )
        current = self.session_events(daemon, session_a)
        self.check(
            [event["id"] for event in current[: len(after_load)]] == after_load,
            "post-load keyed prompt rewrote earlier events",
        )
        self.check(
            [
                payload["text"]
                for event in current
                for name, payload in [_kind(event)]
                if name == "UserText"
            ].count("keyed after load")
            == 1,
            "post-load keyed prompt was not persisted once",
        )
        self.check(
            self.trace_count("keyed", "prompt") == before_restart_prompts + 1,
            "post-load keyed prompt was not executed exactly once",
        )

        self.stop_daemon(daemon, graceful=True)
        self.check_fixture_cleanup("keyed")

    def scenario_isolation_auth_and_protocol(self) -> None:
        main = self.start_daemon("main", event_capacity=1)
        isolated = self.start_daemon("isolated", event_capacity=8)
        self.check(main.token != isolated.token, "two homes shared an auth token")
        self.check(
            self.ctl(isolated, "workspace", "list") == [],
            "a fresh home inherited workspaces",
        )

        workspace_path = self.work / "workspace-isolated"
        workspace_path.mkdir()
        isolated_workspace = self.ctl(
            isolated, "workspace", "add", "Isolated", str(workspace_path)
        )
        main_workspaces = self.ctl(main, "workspace", "list")
        self.check(
            all(item["id"] != isolated_workspace["id"] for item in main_workspaces),
            "workspace leaked between homes",
        )

        wrong = self.run_ctl_with(
            isolated.home,
            isolated.socket,
            ["health"],
            token="definitely-wrong",
        )
        self.check(
            wrong.returncode != 0 and "authentication failed" in wrong.stderr,
            f"wrong auth token was not rejected: {wrong.stderr}",
        )
        empty_home = self.root / "empty-home"
        empty_home.mkdir()
        missing = self.run_ctl_with(
            empty_home,
            isolated.socket,
            ["health"],
            token=None,
        )
        self.check(
            missing.returncode != 0 and "authentication failed" in missing.stderr,
            f"missing auth token was not rejected: {missing.stderr}",
        )

        unavailable = self.ctl_error(
            main,
            "session",
            "new",
            main_workspaces[0]["id"],
            "--provider",
            "claude",
        )
        self.check(
            "unavailable" in (unavailable.stderr + unavailable.stdout),
            f"unavailable provider was not rejected: {unavailable.stderr}",
        )

        response = self.ipc_request(
            isolated.socket,
            isolated.token,
            {
                "version": 1,
                "id": 1,
                "auth_token": isolated.token,
                "command": {
                    "method": "hello",
                    "params": {"min_version": 999, "max_version": 999},
                },
            },
        )
        self.check(
            response.get("error", {}).get("code") == -32001,
            f"incompatible protocol was not rejected: {response}",
        )

        self.stop_daemon(isolated, graceful=True)
        self.stop_daemon(main, graceful=True)
        self.check_fixture_cleanup("isolated")
        self.check_fixture_cleanup("main")

    @staticmethod
    def ipc_request(socket_path: Path, token: str, payload: dict) -> dict:
        frame = (json.dumps(payload) + "\n").encode()
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            client.settimeout(10)
            client.connect(str(socket_path))
            client.sendall(frame)
            buffer = b""
            while b"\n" not in buffer:
                chunk = client.recv(65536)
                if not chunk:
                    break
                buffer += chunk
        if not buffer:
            raise SmokeFailure("daemon closed the connection without a response")
        first = buffer.split(b"\n", 1)[0]
        try:
            return json.loads(first)
        except json.JSONDecodeError as error:
            raise SmokeFailure(f"invalid IPC response: {first!r}") from error

    # ----------------------------------------------------------------- redaction

    def scenario_redaction(self) -> None:
        daemon = self.start_daemon("redaction", event_capacity=8, echo_secret=True)
        workspace_path = self.work / "workspace-redaction"
        workspace_path.mkdir()
        workspace = self.ctl(
            daemon, "workspace", "add", "Redaction", str(workspace_path)
        )
        created = self.ctl(
            daemon,
            "session",
            "new",
            workspace["id"],
            "--provider",
            "talos",
            "--title",
            "Redaction",
        )
        session_id = created["session"]["id"]
        run = self.start_prompt(daemon, session_id, "secret-exit")
        state = self.wait_run_terminal(daemon, session_id, run)
        self.check(state == "Failed", f"secret-exit run ended as {state}")
        errors = [
            payload["message"]
            for event in self.session_events(daemon, session_id)
            for name, payload in [_kind(event)]
            if name == "Error"
        ]
        joined = "\n".join(errors)
        self.check(
            self.secret not in joined,
            "provider environment secret leaked into the durable timeline",
        )
        self.check(
            "[REDACTED]" in joined,
            f"provider stderr was not redacted in the error event: {joined}",
        )
        daemon.log_file.flush()
        log = daemon.log_path.read_text(encoding="utf-8", errors="replace")
        self.check(self.secret not in log, "provider secret leaked into the daemon log")
        diagnostic = self.ctl(daemon, "provider", "diagnostic", "talos")
        self.check(
            self.secret not in json.dumps(diagnostic),
            "provider secret leaked into diagnostics",
        )
        self.stop_daemon(daemon, graceful=True)
        self.check_fixture_cleanup("redaction")

    # --------------------------------------------------------------------- trace

    def trace_count(self, name: str, event: str) -> int:
        return sum(1 for entry in self.trace(name) if entry.get("event") == event)

    def trace(self, name: str) -> list[dict]:
        path = self.traces / f"{name}.jsonl"
        if not path.exists():
            return []
        entries = []
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(entry, dict):
                entries.append(entry)
        return entries

    def check_fixture_cleanup(self, name: str) -> None:
        pids = {
            int(entry["data"]["pid"])
            for entry in self.trace(name)
            if entry.get("event") == "start"
            and isinstance(entry.get("data"), dict)
            and isinstance(entry["data"].get("pid"), int)
        }
        if not pids:
            return

        def probe():
            alive = [pid for pid in pids if self._pid_alive(pid)]
            return True if not alive else None

        try:
            self.wait_until(f"fixture children for {name} to exit", probe, 10)
        except SmokeFailure as error:
            alive = sorted(pid for pid in pids if self._pid_alive(pid))
            raise SmokeFailure(
                f"fixture children still alive for {name}: {alive}"
            ) from error

    @staticmethod
    def _pid_alive(pid: int) -> bool:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    # ------------------------------------------------------------------ cleanup

    def cleanup(self) -> None:
        for daemon in list(self.daemons):
            try:
                self.stop_daemon(daemon, graceful=False)
            except Exception:
                pass
        for trace in self.traces.glob("*.jsonl"):
            for entry in self.trace(trace.stem):
                if entry.get("event") != "start":
                    continue
                data = entry.get("data") or {}
                pid = data.get("pid")
                if isinstance(pid, int) and self._pid_alive(pid):
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except (ProcessLookupError, PermissionError):
                        pass
        if self.args.keep:
            print(f"[smoke] kept temporary root at {self.root}", file=sys.stderr)
            return
        shutil.rmtree(self.root, ignore_errors=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--verbose", action="store_true", help="print progress")
    parser.add_argument(
        "--keep", action="store_true", help="keep the temporary home for debugging"
    )
    parser.add_argument(
        "--scenario",
        action="append",
        choices=SCENARIOS,
        help="run only the named scenario (repeatable; default: all)",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=0.0,
        help="raise and run cleanup after this many seconds (0 disables)",
    )
    args = parser.parse_args()
    if args.timeout > 0:
        def _deadline(_signum, _frame):
            raise SmokeFailure(f"smoke harness exceeded {args.timeout:.0f}s")

        signal.signal(signal.SIGALRM, _deadline)
        signal.alarm(max(1, round(args.timeout)))
    harness = None
    failure = None
    try:
        harness = Harness(args)
        harness.run()
    except SmokeFailure as error:
        failure = error
    except KeyboardInterrupt:
        failure = SmokeFailure("interrupted before completion")
    except Exception as error:  # pragma: no cover - defensive harness failure
        failure = error
        import traceback

        traceback.print_exc()
    finally:
        signal.alarm(0)
        if harness is not None:
            if failure is not None:
                for daemon in harness.daemons:
                    harness.dump_log(daemon)
                harness.log(f"temporary root was {harness.root}")
            harness.cleanup()
    if failure is not None:
        print(f"SMOKE FAILED: {failure}", file=sys.stderr)
        return 1
    print("SMOKE OK: daemon + CLI process-level scenarios passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
