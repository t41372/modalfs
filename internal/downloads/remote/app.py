"""modalfs remote downloads: the Modal Function, and the agent that runs in it.

modalfs embeds this file and deploys it to the user's workspace (see
internal/downloads/deploy.go). The daemon spawns `download(job)` with the
user's volume mounted at /vol; the agent runs one downloader (gopeed, aria2
or yt-dlp) into a staging folder on the volume, keeps a durable record next
to it, serves live progress through a Modal tunnel, and moves the finished
files into the folder the user picked.

On the volume, per job (docs/design/remote-downloads.md):

    .modalfs/downloads/<id>/
      status.json   latest state, rewritten every 5 s, then `sync`
      events.jsonl  one line per start, interruption, retry, pause, finish
      speed/        one [t, done, speed] line per second, in 30 s chunks
      state/        the downloader's own state (gopeed's database)
      work/         files being downloaded
      log.txt       the downloader's log, copied when the run ends

The agent's core runs without Modal: `python3 app.py run job.json --root DIR`
uses a local folder as the volume. The tests do that (test_app.py).
"""

import contextlib
import hmac
import http.server
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from typing import Optional

GOPEED_VERSION = "v2.0.0-beta.3"
GOPEED_SHA256 = "230f703d8147df28b68c162c4398e12d8e57412d43584f7312fa3532c2bd3c8e"
DENO_VERSION = "v2.9.7"
DENO_SHA256 = "c6527f24f4b16031d3ae4fa9f658d5f11534c8d84ce7dc8502420280919c3490"

SCHEMA = 1
STATUS_EVERY = 5.0  # seconds between status.json writes (each followed by sync)
SPEED_CHUNK = 30.0  # seconds of speed history per chunk file
KICK_AFTER = 60.0  # seconds without progress before the downloader is restarted
RETRIES = 5  # times a failed download is continued before giving up on the tool
PROGRESS_RESETS_RETRIES = 64 << 20  # bytes downloaded after an error that make it a fresh start
CONTROL_FILES = (".aria2", ".part", ".ytdl", ".part-Frag")

# Tests shorten STATUS_EVERY, SPEED_CHUNK and KICK_AFTER through the job
# (status_every, speed_chunk, kick_after); modalfs doesn't.

# Set by modalfs when it deploys (MODALFS_* are only read at deploy time).
APP_NAME = os.environ.get("MODALFS_APP_NAME", "modalfs-downloads")
VERSION = os.environ.get("MODALFS_AGENT_VERSION", "")


def now():
    return time.time()


class Stop(Exception):
    """Ends a run early with a final state: paused, stalled, failed, interrupted."""

    def __init__(self, state, message=""):
        super().__init__(message or state)
        self.state = state
        self.message = message


class Progress:
    def __init__(self, state="running", done=0, total=-1, speed=0, name="", note="", error=""):
        self.state = state  # running, paused, done, error
        self.done = done
        self.total = total  # -1 when unknown
        self.speed = speed  # bytes per second
        self.name = name
        self.note = note
        self.error = error


# ---------------------------------------------------------------- helpers


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_json(method, url, body=None, headers=None, timeout=10):
    req = urllib.request.Request(
        url,
        method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", **(headers or {})},
    )
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read() or b"null")


def tail(path, lines=50):
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            f.seek(max(0, f.tell() - 64 * 1024))
            return f.read().decode("utf-8", "replace").splitlines()[-lines:]
    except OSError:
        return []


def wait_until(check, timeout, step=0.1):
    end = now() + timeout
    while now() < end:
        if check():
            return True
        time.sleep(step)
    return check()


def is_control_file(name):
    return name.endswith(CONTROL_FILES) or ".part-Frag" in name


def strip_credentials(url):
    """The link without user:password@ and its query string (signed URLs,
    tracker passkeys), for records on the volume."""
    if url.startswith("magnet:"):
        m = re.search(r"[?&](xt=[^&]+)", url)
        return "magnet:?" + m.group(1) if m else "magnet:"
    m = re.match(r"^([a-z][a-z0-9+.-]*://)(?:[^@/]*@)?([^?#]*)", url, re.I)
    return m.group(1) + m.group(2) if m else url.split("?")[0]


def write_file(path, data):
    # In place, not write-and-rename: on a Modal volume a renamed file can
    # read back as zeros for seconds; an in-place write followed by `sync`
    # reads back whole (measured in the spike).
    with open(path, "w") as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())


def stop_process(proc, sig=signal.SIGINT, timeout=10):
    if proc is None or proc.poll() is not None:
        return
    with contextlib.suppress(ProcessLookupError):
        os.killpg(proc.pid, sig)
    try:
        proc.wait(timeout)
    except subprocess.TimeoutExpired:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(proc.pid, signal.SIGKILL)
        proc.wait(5)


def start_process(argv, log_path, cwd=None):
    # Own process group: Modal's SIGINT goes to the agent, which pauses the
    # tool through its API instead of letting the signal kill it mid-write.
    with open(log_path, "ab") as log:
        return subprocess.Popen(
            argv,
            cwd=cwd,
            start_new_session=True,
            stdin=subprocess.DEVNULL,
            stdout=log,
            stderr=subprocess.STDOUT,
        )


# ---------------------------------------------------------------- tools


class Tool:
    """One downloader. start() picks up whatever earlier runs left in work/
    and state/; poll() reports progress; pause() leaves everything resumable."""

    name = ""

    def __init__(self, job, work, state, log):
        self.job, self.work, self.state, self.log = job, work, state, log
        os.makedirs(state, exist_ok=True)

    def start(self):
        raise NotImplementedError

    def poll(self) -> Progress:
        raise NotImplementedError

    def pause(self):
        raise NotImplementedError

    def kick(self):
        """Reopen connections after a stall."""
        self.pause()
        self.resume()

    def resume(self):
        raise NotImplementedError

    def retry(self):
        """Continue after an error. Returns False if the tool can't."""
        return False

    def discard(self):
        """Give up on this tool's partial download (before a fallback)."""
        self.stop()

    def stop(self):
        raise NotImplementedError

    def logs(self):
        """The tool's log files, oldest first."""
        return [self.log]


class Gopeed(Tool):
    name = "gopeed"

    def __init__(self, job, work, state, log, binary="gopeed"):
        super().__init__(job, work, state, log)
        self.binary = binary
        self.proc = None
        self.task_file = os.path.join(state, "task-id")

    def api(self, method, path, body=None):
        r = http_json(method, f"{self.base}{path}", body, {"X-Api-Token": self.token})
        if r.get("code", 0) != 0:
            raise RuntimeError(f"gopeed {method} {path}: {r.get('msg')}")
        return r.get("data")

    def start(self):
        self.token = secrets.token_hex(16)
        port = free_port()
        self.base = f"http://127.0.0.1:{port}"
        # The web build insists on a username and password whenever an API
        # token is set; nothing but the agent can reach it anyway.
        argv = [self.binary, "-A", "127.0.0.1", "-P", str(port), "-T", self.token,
                "-u", "modalfs", "-p", secrets.token_hex(16), "-d", self.state]
        self.proc = start_process(argv, self.log, cwd=self.state)
        if not wait_until(self.alive, 30):
            raise RuntimeError("gopeed didn't start: " + " | ".join(tail(self.log, 5)))
        cfg = self.api("GET", "/api/v1/config")
        bt = cfg.setdefault("protocolConfig", {}).setdefault("bt", {})
        bt.update(seedKeep=False, seedRatio=0, seedTime=0)
        cfg["autoStartTasks"] = False
        self.api("PUT", "/api/v1/config", cfg)

        self.id = ""
        if os.path.exists(self.task_file):
            self.id = open(self.task_file).read().strip()
            try:
                task = self.api("GET", f"/api/v1/tasks/{self.id}")
            except (RuntimeError, urllib.error.HTTPError):
                task = None
            if task is None:
                self.id = ""  # its database was lost: start over (work/ is cleared below)
            elif task["status"] != "done":
                self.api("PUT", f"/api/v1/tasks/{self.id}/continue")
        if not self.id:
            clear_dir(self.work)
            extra = {"connections": int(self.job.get("connections", 16))}
            self.id = self.api("POST", "/api/v1/tasks", {
                "req": {"url": self.job["url"]},
                "opts": {"path": self.work, "extra": extra},
            })
            write_file(self.task_file, self.id)

    def alive(self):
        try:
            http_json("GET", f"{self.base}/api/v1/info", headers={"X-Api-Token": self.token}, timeout=2)
            return True
        except (OSError, ValueError):
            return False

    def poll(self):
        if self.proc.poll() is not None:
            return Progress("error", error="gopeed exited: " + " | ".join(tail(self.log, 5)))
        t = self.api("GET", f"/api/v1/tasks/{self.id}")
        p = t.get("progress") or {}
        res = (t.get("meta") or {}).get("res") or {}
        total = res.get("size") or sum(f.get("size", 0) for f in res.get("files") or []) or -1
        state = {"pause": "paused", "error": "error", "done": "done"}.get(t["status"], "running")
        return Progress(state, p.get("downloaded", 0), total, p.get("speed", 0), t.get("name", ""),
                        error=self.last_error() if state == "error" else "")

    def logs(self):
        # with a storage dir, gopeed logs to <storage>/logs/core.log, not stdout
        return [self.log, os.path.join(self.state, "logs", "core.log")]

    def last_error(self):
        for line in reversed(tail(self.logs()[1], 200) + tail(self.log, 200)):
            m = re.search(r'error="(.*)"', line)
            if m:
                return m.group(1)
        return "gopeed reported an error without a message"

    def pause(self):
        self.api("PUT", f"/api/v1/tasks/{self.id}/pause")
        wait_until(lambda: self.poll().state != "running", 10, 0.2)

    def resume(self):
        self.api("PUT", f"/api/v1/tasks/{self.id}/continue")

    def retry(self):
        self.resume()
        return True

    def discard(self):
        with contextlib.suppress(Exception):
            self.api("DELETE", f"/api/v1/tasks/{self.id}?force=true")
        self.stop()
        with contextlib.suppress(FileNotFoundError):
            os.remove(self.task_file)
        clear_dir(self.work)

    def stop(self):
        stop_process(self.proc)


class Aria2(Tool):
    name = "aria2"

    def __init__(self, job, work, state, log, binary="aria2c"):
        super().__init__(job, work, state, log)
        self.binary = binary
        self.proc = None

    def rpc(self, method, *params):
        r = http_json("POST", f"http://127.0.0.1:{self.port}/jsonrpc",
                      {"jsonrpc": "2.0", "id": "m", "method": method, "params": [f"token:{self.secret}", *params]})
        return r["result"]

    def start(self):
        self.port, self.secret = free_port(), secrets.token_hex(16)
        argv = [self.binary, "--enable-rpc", f"--rpc-listen-port={self.port}", "--rpc-listen-all=false",
                f"--rpc-secret={self.secret}", f"--dir={self.work}", "--continue=true",
                "--max-connection-per-server=16", "--split=16", "--min-split-size=4M",
                "--file-allocation=none", "--auto-save-interval=1", "--max-tries=0", "--retry-wait=5",
                "--allow-overwrite=false", "--auto-file-renaming=false", "--seed-time=0",
                "--bt-save-metadata=true", "--console-log-level=warn", "--summary-interval=0"]
        if self.job.get("limit"):
            argv.append(f"--max-overall-download-limit={self.job['limit']}")
        self.proc = start_process(argv, self.log)
        if not wait_until(self.alive, 30):
            raise RuntimeError("aria2 didn't start: " + " | ".join(tail(self.log, 5)))
        # Added again on every run: aria2 resumes from the .aria2 control
        # files next to the data.
        self.gid = self.rpc("aria2.addUri", [self.job["url"]])

    def alive(self):
        try:
            self.rpc("aria2.getVersion")
            return True
        except (OSError, ValueError, KeyError):
            return False

    def poll(self):
        if self.proc.poll() is not None:
            return Progress("error", error="aria2 exited: " + " | ".join(tail(self.log, 5)))
        s = self.rpc("aria2.tellStatus", self.gid, ["status", "totalLength", "completedLength",
                                                     "downloadSpeed", "errorMessage", "followedBy", "files"])
        if s["status"] == "complete" and s.get("followedBy"):
            self.gid = s["followedBy"][0]  # a magnet or .torrent turned into the real download
            return Progress("running", note="fetching the file list")
        state = {"active": "running", "waiting": "running", "paused": "paused",
                 "complete": "done", "error": "error", "removed": "error"}[s["status"]]
        files = s.get("files") or []
        name = os.path.basename(files[0]["path"]) if files and files[0].get("path") else ""
        total = int(s["totalLength"]) or -1
        return Progress(state, int(s["completedLength"]), total, int(s["downloadSpeed"]), name,
                        error=s.get("errorMessage", "") if state == "error" else "")

    def pause(self):
        # aria2 refuses to pause a download that isn't active (still waiting
        # to start, say); its .aria2 control file is what a resume needs.
        try:
            self.rpc("aria2.pause", self.gid)
        except urllib.error.HTTPError:
            if self.rpc("aria2.tellStatus", self.gid, ["status"])["status"] == "active":
                raise
            return
        wait_until(lambda: self.poll().state != "running", 10, 0.2)

    def resume(self):
        self.rpc("aria2.unpause", self.gid)

    def stop(self):
        with contextlib.suppress(Exception):
            self.rpc("aria2.shutdown")
        stop_process(self.proc)


class YtDlp(Tool):
    name = "yt-dlp"
    MARK = "MODALFS "

    def __init__(self, job, work, state, log, binary="yt-dlp"):
        super().__init__(job, work, state, log)
        self.binary = binary
        self.proc = None
        self.lock = threading.Lock()
        self.files = {}  # filename -> [downloaded, total]
        self.speed = 0
        self.last = ""

    def start(self):
        if self.job.get("update_ytdlp"):
            # The latest release every run: sites change faster than modalfs.
            r = subprocess.run([sys.executable, "-m", "pip", "install", "-q", "-U", "yt-dlp[default]"],
                               capture_output=True, text=True)
            if r.returncode != 0:
                raise RuntimeError("couldn't install yt-dlp: " + r.stderr[-500:])
        self.run()

    def run(self):
        argv = [self.binary, self.job["url"], "-P", self.work, "--newline", "--continue", "--no-overwrites",
                "--no-color", "--progress-template", f"download:{self.MARK}%(progress)j",
                "-o", "%(title).150B [%(id)s].%(ext)s"]
        if self.job.get("cookies"):
            path = os.path.join("/tmp", "modalfs-cookies.txt")  # never on the volume
            write_file(path, self.job["cookies"])
            argv += ["--cookies", path]
        self.proc = subprocess.Popen(argv, start_new_session=True, stdin=subprocess.DEVNULL,
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        threading.Thread(target=self.read, args=(self.proc,), daemon=True).start()

    def read(self, proc):
        with open(self.log, "a") as log:
            for line in proc.stdout:
                if not line.startswith(self.MARK):
                    log.write(line)
                    log.flush()
                    self.last = line.strip()
                    continue
                try:
                    p = json.loads(line[len(self.MARK):])
                except ValueError:
                    continue
                total = p.get("total_bytes") or p.get("total_bytes_estimate") or -1
                with self.lock:
                    self.files[p.get("filename", "")] = [p.get("downloaded_bytes") or 0, int(total)]
                    self.speed = int(p.get("speed") or 0)

    def poll(self):
        code = self.proc.poll()
        with self.lock:
            done = sum(d for d, _ in self.files.values())
            totals = [t for _, t in self.files.values()]
            total = sum(totals) if totals and all(t > 0 for t in totals) else -1
            speed = self.speed if code is None else 0
            name = os.path.basename(next(reversed(self.files), "")) if self.files else ""
        if code is None:
            return Progress("running", done, total, speed, name)
        if code == 0:
            return Progress("done", done, total, 0, name)
        if getattr(self, "paused", False):
            return Progress("paused", done, total, 0, name)
        return Progress("error", done, total, 0, name, error=self.last or f"yt-dlp exited with {code}")

    def pause(self):
        self.paused = True
        stop_process(self.proc, signal.SIGINT, 30)

    def resume(self):
        self.paused = False
        self.run()

    def retry(self):
        self.resume()
        return True

    def stop(self):
        self.pause()


TOOLS = {"gopeed": Gopeed, "aria2": Aria2, "yt-dlp": YtDlp}


def clear_dir(path):
    shutil.rmtree(path, ignore_errors=True)
    os.makedirs(path, exist_ok=True)


# ---------------------------------------------------------------- record


class Record:
    """The job's files on the volume, and the in-memory state the live
    channel serves. Writes of status.json and speed chunks happen on a
    background thread, because `sync` on a Modal volume takes about a second."""

    def __init__(self, jobdir, env, job):
        self.dir, self.env, self.job = jobdir, env, job
        self.lock = threading.Lock()
        self.status = {"v": SCHEMA, "id": job["id"], "state": "starting", "tool": job.get("tool", "gopeed"),
                       "url": strip_credentials(job["url"]), "done": 0, "total": -1, "speed": 0,
                       "t": now()}
        self.history = []  # [t, done, speed] once a second, this run
        self.flushed = 0  # how much of history is in chunk files
        os.makedirs(os.path.join(jobdir, "speed"), exist_ok=True)
        self.chunk = len(os.listdir(os.path.join(jobdir, "speed")))
        self.closed = threading.Event()
        self.sync_times = []
        self.writer = threading.Thread(target=self.write_loop, daemon=True)
        self.writer.start()

    def previous(self):
        try:
            with open(os.path.join(self.dir, "status.json")) as f:
                return json.load(f)
        except (OSError, ValueError):
            return None

    def events(self):
        try:
            with open(os.path.join(self.dir, "events.jsonl")) as f:
                return [json.loads(line) for line in f if line.strip()]
        except (OSError, ValueError):
            return []

    def event(self, kind, **fields):
        line = json.dumps({"ev": kind, "t": now(), **fields})
        with open(os.path.join(self.dir, "events.jsonl"), "a") as f:
            f.write(line + "\n")
            f.flush()
            os.fsync(f.fileno())
        self.env.sync()

    def update(self, **fields):
        with self.lock:
            self.status.update(fields, t=now())

    def sample(self, p: Progress):
        t = round(now(), 1)
        eta = int((p.total - p.done) / p.speed) if p.speed > 0 and p.total > 0 else None
        with self.lock:
            self.history.append([t, p.done, p.speed])
            self.status.update(done=p.done, total=p.total, speed=p.speed, eta=eta, t=t)
            if p.name:
                self.status["name"] = p.name
            if p.note:
                self.status["note"] = p.note

    def snapshot(self):
        with self.lock:
            return dict(self.status)

    def history_since(self, t0=0):
        with self.lock:
            return [h for h in self.history if h[0] > t0]

    def write_loop(self):
        while not self.closed.wait(float(self.job.get("status_every", STATUS_EVERY))):
            self.write()

    def write(self, final=False):
        with self.lock:
            status = dict(self.status)
            rows = self.history[self.flushed:]
            # a chunk holds SPEED_CHUNK seconds; the last one whatever is left
            if not final and (not rows or rows[-1][0] - rows[0][0] < float(self.job.get("speed_chunk", SPEED_CHUNK))):
                rows = []
            self.flushed += len(rows)
        if self.sync_times:
            status["sync_avg"] = round(sum(self.sync_times) / len(self.sync_times), 3)
            status["sync_max"] = round(max(self.sync_times), 3)
        if rows:
            self.write_chunk(rows)
        write_file(os.path.join(self.dir, "status.json"), json.dumps(status))
        t = now()
        self.env.sync()
        self.sync_times = (self.sync_times + [now() - t])[-100:]
        return status

    def write_chunk(self, rows):
        path = os.path.join(self.dir, "speed", f"{self.chunk:06d}.jsonl")
        self.chunk += 1
        write_file(path, "".join(json.dumps(r) + "\n" for r in rows))

    def close(self, **fields):
        """Writes the final status and the rest of the history, then syncs."""
        self.closed.set()
        self.writer.join(30)
        self.update(**fields)
        return self.write(final=True)


# ---------------------------------------------------------------- live channel


class Live:
    """GET /live: server-sent events, 4 a second. GET /history: this run's
    speed history. POST /pause: pause and end the run. All need the job's
    token in the X-Modalfs-Token header."""

    def __init__(self, record, token, on_pause):
        class Handler(http.server.BaseHTTPRequestHandler):
            def authorized(self):
                got = self.headers.get("X-Modalfs-Token", "")
                if hmac.compare_digest(got.encode(), token.encode()):
                    return True
                self.send_response(403)
                self.end_headers()
                return False

            def send_json(self, code, obj):
                body = json.dumps(obj).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if not self.authorized():
                    return
                if self.path == "/history":
                    self.send_json(200, record.history_since())
                    return
                if self.path != "/live":
                    self.send_json(404, {"error": "not found"})
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
                try:
                    while True:
                        s = record.snapshot()
                        self.wfile.write(f"data: {json.dumps(s)}\n\n".encode())
                        self.wfile.flush()
                        if s["state"] not in ("starting", "running", "retrying", "pausing", "finishing"):
                            return
                        time.sleep(0.25)
                except (BrokenPipeError, ConnectionResetError):
                    return

            def do_POST(self):
                if not self.authorized():
                    return
                if self.path != "/pause":
                    self.send_json(404, {"error": "not found"})
                    return
                on_pause()
                self.send_json(202, {"state": "pausing"})

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Handler)
        self.server.daemon_threads = True
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()


# ---------------------------------------------------------------- the agent


class LocalEnv:
    """Running outside Modal: a folder stands in for the volume."""

    in_modal = False

    def check_root(self, root, job):
        if not os.path.isdir(root):
            raise RuntimeError(f"{root} is not a folder")

    @contextlib.contextmanager
    def forward(self, port):
        yield f"http://127.0.0.1:{port}"

    def sync(self):
        pass

    def binary(self, tool):
        env = {"gopeed": "GOPEED_BIN", "aria2": "ARIA2C_BIN", "yt-dlp": "YTDLP_BIN"}[tool]
        return os.environ.get(env) or {"aria2": "aria2c"}.get(tool, tool)

    def task_id(self):
        return f"local-{os.getpid()}"

    def after_signal(self):
        pass

    def spawn_successor(self, job):
        raise RuntimeError("continuing in a new Function is only possible on Modal")


class Agent:
    def __init__(self, job, root, env):
        self.job, self.root, self.env = job, root, env
        self.jobdir = os.path.join(root, ".modalfs", "downloads", job["id"])
        self.work = os.path.join(self.jobdir, "work")
        self.state = os.path.join(self.jobdir, "state")
        # off the volume while the tools write to it; copied to log.txt at the end
        self.log = os.path.join(tempfile.mkdtemp(prefix="modalfs-agent-"), "tools.log")
        self.signalled = threading.Event()
        self.pause_requested = threading.Event()
        timeout = float(job.get("timeout", 24 * 3600))
        self.deadline = now() + timeout - min(600.0, timeout * 0.25)

    def run(self):
        job, env = self.job, self.env
        env.check_root(self.root, job)
        os.makedirs(self.work, exist_ok=True)
        self.record = rec = Record(self.jobdir, env, job)
        self.note_previous_run()
        if not os.path.exists(os.path.join(self.jobdir, "job.json")):
            public = {k: v for k, v in job.items() if k not in ("live_token", "cookies")}
            public["url"] = strip_credentials(job["url"])
            write_file(os.path.join(self.jobdir, "job.json"), json.dumps(public))

        live = Live(rec, job["live_token"], self.pause_requested.set)
        try:
            with env.forward(live.port) as url:
                rec.update(state="starting", live_url=url, attempt=self.attempt, task=env.task_id())
                return self.download()
        finally:
            live.close()

    def note_previous_run(self):
        rec = self.record
        events = rec.events()
        self.attempt = 1 + sum(1 for e in events if e["ev"] == "start")
        prev = rec.previous()
        rec.event("start", attempt=self.attempt, task=self.env.task_id())
        if prev and prev.get("state") in ("starting", "running", "retrying"):
            # The last run ended without a word: the container was lost.
            rec.event("interrupted", warned=False, last_status=prev.get("t"), done=prev.get("done", 0))
        elif prev and prev.get("state") == "interrupted":
            rec.event("resumed", gap=round(now() - prev.get("t", now()), 1), done=prev.get("done", 0))

    def make_tool(self, name):
        cls = TOOLS.get(name)
        if cls is None:
            raise Stop("failed", f"unknown downloader {name!r}")
        return cls(self.job, self.work, os.path.join(self.state, name), self.log, binary=self.env.binary(name))

    def download(self):
        rec, job = self.record, self.job
        # A fallback sticks: later runs continue with the tool that has the
        # partial download, not the one the job asked for.
        chosen = os.path.join(self.state, "tool")
        name = job.get("tool", "gopeed")
        if os.path.exists(chosen):
            name = open(chosen).read().strip() or name
        tool = self.make_tool(name)
        self.tool = tool
        retries, stalled_since, kicked, done_at_error = 0, None, False, 0
        stall_limit = float(job.get("stall_limit", 600))
        kick_after = float(job.get("kick_after", KICK_AFTER))
        try:
            tool.start()
            rec.update(state="running", tool=name)
            while True:
                p = tool.poll()
                rec.sample(p)
                if p.state == "done":
                    return self.finish(tool)
                if self.signalled.is_set():
                    return self.save_and_end(tool, "interrupted")
                if self.pause_requested.is_set():
                    return self.save_and_end(tool, "paused")
                if now() >= self.deadline:
                    return self.time_limit(tool)
                if p.state == "error":
                    retries += 1
                    done_at_error = p.done
                    rec.event("error", tool=tool.name, message=p.error, retry=retries)
                    if retries <= RETRIES and tool.retry():
                        rec.update(state="retrying", error=p.error)
                        time.sleep(2 * retries)
                        continue
                    if tool.name == "gopeed" and job["url"].startswith(("http://", "https://")):
                        rec.event("fallback", tool="aria2", reason=p.error)
                        tool.discard()
                        write_file(chosen, "aria2")
                        tool = self.tool = self.make_tool("aria2")
                        tool.start()
                        retries = 0
                        rec.update(state="running", tool="aria2", error="")
                        continue
                    raise Stop("failed", p.error)
                if p.state == "running" and retries:
                    rec.update(state="running", error="")
                    # errors far apart are each worth continuing after; only
                    # errors in a row, with nothing gained, give up on the tool
                    if p.done - done_at_error >= PROGRESS_RESETS_RETRIES:
                        retries = 0
                # Stall watchdog: reopen connections once, then stop paying for a dead server.
                if p.state == "running" and p.speed == 0:
                    stalled_since = stalled_since or now()
                    idle = now() - stalled_since
                    if idle >= kick_after and not kicked:
                        rec.event("stalled", seconds=round(idle), action="reconnect")
                        tool.kick()
                        kicked = True
                    elif idle >= stall_limit:
                        rec.event("stalled", seconds=round(idle), action="stop")
                        self.pause_tool(tool)
                        raise Stop("stalled", f"no data for {int(idle)} s")
                else:
                    stalled_since, kicked = None, False
                time.sleep(1)
        except Stop as e:
            return self.end(tool, e.state, e.message)
        except Exception as e:  # report it on the volume, then let Modal see the failure
            self.end(tool, "failed", f"{type(e).__name__}: {e}")
            raise

    def save_and_end(self, tool, state):
        self.record.update(state="pausing")
        self.pause_tool(tool)
        self.record.event("signal" if state == "interrupted" else "paused", done=self.record.snapshot()["done"])
        summary = self.end(None, state)
        if state == "interrupted":
            self.env.after_signal()
        return summary

    def time_limit(self, tool):
        self.pause_tool(tool)
        if self.job.get("respawn", True):
            try:
                call = self.env.spawn_successor(self.job)
                self.record.event("continued", call=call)
                return self.end(None, "continued", next_call=call)
            except Exception as e:
                self.record.event("error", message=f"couldn't continue in a new Function: {e}")
        return self.end(None, "time limit", "paused at the Function's time limit")

    def pause_tool(self, tool):
        try:
            tool.pause()
        except Exception as e:  # its saved state is whatever it last wrote
            self.record.event("error", message=f"couldn't pause {tool.name} cleanly: {e}")
        tool.stop()

    def end(self, tool, state, message="", **fields):
        if tool is not None:
            with contextlib.suppress(Exception):
                tool.stop()
        if state in ("failed", "stalled"):
            fields["log_tail"] = [line for path in self.logs() for line in tail(path, 25)]
        if state == "failed":
            self.record.event("failed", message=message)
        if not getattr(self, "copied", False):
            self.copy_log()
        status = self.record.close(state=state, error=message, **fields)
        return {k: status.get(k) for k in ("id", "state", "error", "done", "total", "files", "next_call", "attempt")
                if status.get(k) is not None}

    def logs(self):
        tool = getattr(self, "tool", None)
        return tool.logs() if tool else [self.log]

    def copy_log(self):
        # the logs live outside the volume while the tool runs (/tmp in
        # Modal, or its state folder, removed when the download finishes)
        with open(os.path.join(self.jobdir, "log.txt"), "a") as out:
            for path in self.logs():
                with contextlib.suppress(OSError), open(path, errors="replace") as f:
                    out.write(f"--- {os.path.basename(path)}\n")
                    shutil.copyfileobj(f, out)

    def finish(self, tool):
        tool.stop()
        self.record.update(state="finishing")
        root = os.path.realpath(self.root)
        dest = safe_join(root, self.job.get("folder", ""))
        os.makedirs(dest, exist_ok=True)
        moved = []
        for src in sorted(downloaded_files(self.work)):
            rel = os.path.relpath(src, self.work)
            target = unique_path(safe_join(dest, rel))
            os.makedirs(os.path.dirname(target), exist_ok=True)
            os.rename(src, target)
            entry = {"path": os.path.relpath(target, root), "size": os.path.getsize(target)}
            if self.job.get("hash", True):
                entry["sha256"] = sha256(target)
            moved.append(entry)
        if not moved:
            raise Stop("failed", "the downloader finished but left no files")
        self.record.event("finished", files=moved)
        self.copy_log()
        self.copied = True
        shutil.rmtree(self.work, ignore_errors=True)
        shutil.rmtree(self.state, ignore_errors=True)
        return self.end(None, "done", files=moved)


def downloaded_files(work):
    for dirpath, dirnames, filenames in os.walk(work, followlinks=False):
        for f in filenames:
            path = os.path.join(dirpath, f)
            if is_control_file(f) or os.path.islink(path):
                continue
            yield path


def safe_join(base, rel):
    """base/rel, refusing anything that would land outside base."""
    base = os.path.realpath(base)
    path = os.path.realpath(os.path.join(base, rel.lstrip("/")))
    if path != base and not path.startswith(base + os.sep):
        raise Stop("failed", f"refusing a path outside the destination: {rel!r}")
    return path


def unique_path(path):
    """path, or "name (1).ext", "name (2).ext"... if it exists. Never overwrites."""
    if not os.path.lexists(path):
        return path
    stem, ext = os.path.splitext(path)
    n = 1
    while os.path.lexists(f"{stem} ({n}){ext}"):
        n += 1
    return f"{stem} ({n}){ext}"


def sha256(path):
    import hashlib

    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def run_job(job, root, env):
    agent = Agent(job, root, env)
    previous = {}
    for sig in (signal.SIGINT, signal.SIGTERM):
        previous[sig] = signal.signal(sig, lambda s, f: agent.signalled.set())
    try:
        return agent.run()
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


# ---------------------------------------------------------------- Modal

try:
    import modal
except ImportError:  # the local tests and `python3 app.py run` don't need it
    modal = None

if modal is not None:
    image = (
        modal.Image.debian_slim(python_version="3.12")
        .apt_install("aria2", "ffmpeg", "curl", "unzip", "ca-certificates")
        .run_commands(
            f"curl -fsSL -o /tmp/g.zip https://github.com/GopeedLab/gopeed/releases/download/{GOPEED_VERSION}/gopeed-web-{GOPEED_VERSION}-linux-amd64.zip",
            f"echo '{GOPEED_SHA256}  /tmp/g.zip' | sha256sum -c -",
            "cd /tmp && unzip -q g.zip && mv gopeed-web-*/gopeed /usr/local/bin/gopeed && rm -rf /tmp/g.zip /tmp/gopeed-web-*",
            f"curl -fsSL -o /tmp/d.zip https://github.com/denoland/deno/releases/download/{DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip",
            f"echo '{DENO_SHA256}  /tmp/d.zip' | sha256sum -c -",
            "cd /tmp && unzip -q d.zip && mv deno /usr/local/bin/deno && rm /tmp/d.zip",
        )
        .env({"MODALFS_APP_NAME": APP_NAME})  # so the agent knows its app when it runs
    )
    app = modal.App(APP_NAME, image=image, tags={"modalfs-agent": VERSION} if VERSION else None)

    class ModalEnv(LocalEnv):
        in_modal = True

        def check_root(self, root, job):
            # /vol is a link to /__modal/volumes/<volume id>; checking it pins
            # the job to the volume it was started for (principle 3).
            want = f"/__modal/volumes/{job['volume_id']}"
            if os.path.realpath(root) != want:
                raise RuntimeError(f"{root} isn't volume {job['volume_id']} (it's {os.path.realpath(root)})")

        def forward(self, port):
            return _forward_url(port)

        def sync(self):
            subprocess.run(["sync", "/vol"], check=False)

        def binary(self, tool):
            return {"aria2": "aria2c"}.get(tool, tool)

        def task_id(self):
            return os.environ.get("MODAL_TASK_ID", "")

        def after_signal(self):
            # Preemption: the state is saved; wait for Modal to stop the
            # container (it does within 30 s) so the input is run again.
            # Returning would end the input as if the download had finished.
            time.sleep(120)
            raise KeyboardInterrupt("still running two minutes after the stop signal")

        def spawn_successor(self, job):
            fn = modal.Function.from_name(job["app"], "download").with_options(
                volumes={"/vol": modal.Volume.from_id(job["volume_id"])},
                cpu=job.get("cpu", 0.125), memory=job.get("memory", 512), timeout=int(job.get("timeout", 86400)),
            )
            nxt = dict(job, chain=int(job.get("chain", 0)) + 1)
            return fn.spawn(nxt).object_id

    @contextlib.contextmanager
    def _forward_url(port):
        with modal.forward(port) as tunnel:
            yield tunnel.url

    @app.function(
        cpu=0.125,
        memory=512,
        timeout=24 * 3600,
        scaledown_window=2,
        single_use_containers=True,
        retries=modal.Retries(max_retries=2, initial_delay=5.0),
        # Results as CBOR, never pickle, even for the calls the agent spawns
        # itself at the time limit: modalfs reads them from Go, which can't
        # unpickle. (Experimental in the Modal client; there's no stable way.)
        _experimental_restrict_output=True,
    )
    def download(job: dict) -> dict:
        return run_job(job, "/vol", ModalEnv())


# ---------------------------------------------------------------- command line


def main(argv):
    if len(argv) >= 2 and argv[0] == "run":
        root = argv[argv.index("--root") + 1] if "--root" in argv else "."
        with open(argv[1]) as f:
            job = json.load(f)
        print(json.dumps(run_job(job, root, LocalEnv())), flush=True)
        return 0
    print("usage: app.py run JOB.json --root DIR", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
