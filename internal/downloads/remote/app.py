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
import pwd
import re
import secrets
import shutil
import signal
import socket
import stat
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
# JavaScript runtime for yt-dlp in the remote download image.
DENO_VERSION = "v2.9.8"
DENO_SHA256 = "e890975bc2549807bbcc0b584ffafda72cc7c93349dd99c80483dd784f6c4609"
# Cloudflare WARP (class Warp): wgcf registers an identity (it mimics the
# official app's TLS handshake, which Cloudflare's API checks), and the
# proxy in warp/ is built with this Go.
WGCF_VERSION = "v2.3.1"
WGCF_SHA256 = "4fb7a136aa470ece11d17949c7db07b040ec9c604742dc8680bcf1889f85d0c8"
GO_VERSION = "1.27.3"
GO_SHA256 = "65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40"
WARP_ERROR = "Cloudflare WARP"  # failures because of WARP start with it; modalfs offers to retry without
PROXY_PORT = 1080  # the WARP proxy, as a downloader in its own network namespace sees it

SCHEMA = 1
STATUS_EVERY = 5.0  # seconds between status.json writes (each followed by sync)
SPEED_CHUNK = 30.0  # seconds of speed history per chunk file
SAMPLE_EVERY = 0.25  # seconds between samples of the downloader: the live channel's rate
SPEED_WINDOW = 2.0  # seconds of progress the speed is measured over
SPEED_SMOOTHING = 0.35  # weight of each new measurement in the shown speed
HISTORY_EVERY = 1.0  # seconds between rows of the speed history
KICK_AFTER = 60.0  # seconds without progress before the downloader is restarted
RETRIES = 5  # times a failed download is continued before giving up on the tool
PROGRESS_RESETS_RETRIES = 64 << 20  # bytes downloaded after an error that make it a fresh start
CONTROL_FILES = (".aria2", ".part", ".ytdl", ".part-Frag")
DOWNLOADER_USER = "dl"  # in the Function's image: the downloaders run as it

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


def tail(path, lines=50, opener=None):
    try:
        with (opener or (lambda p: open(p, "rb")))(path) as f:
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


class Sandbox:
    """Who the downloaders run as. In Modal the agent is root, and any
    process in the container that can open /run/modal.sock has the
    workspace's whole API: every volume, apps, Functions, Sandboxes. The
    downloaders and what they run (yt-dlp is fetched fresh every run) get a
    user of their own that can't open it, an environment with nothing of the
    agent's in it, and write access only to the job's work/ and their own
    state folders. Outside Modal, or not as root, only the environment is
    cleaned (user is None)."""

    PASS_ENV = ("PATH", "LANG", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "http_proxy", "https_proxy",
                "no_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY")

    def __init__(self, user=None):
        self.user = user  # a pwd entry, or None
        if user is not None:
            self.uid, self.gid, self.home = user.pw_uid, user.pw_gid, user.pw_dir
        else:
            self.uid = self.gid = None
            self.home = os.environ.get("HOME", "/tmp")
        self.netns = None  # a NetNS when the job goes through WARP

    def wrap(self, argv, api_port=None):
        """argv as it's started, and the port on the agent's side for a
        server the tool listens on (api_port): the same, or forwarded into
        the tool's network namespace."""
        if self.netns is None:
            return argv, api_port
        return self.netns.wrap(argv, api_port)

    def env(self):
        env = {k: os.environ[k] for k in self.PASS_ENV if k in os.environ}
        env.setdefault("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
        env.setdefault("LANG", "C.UTF-8")
        env["HOME"] = self.home
        return env

    def popen_args(self):
        args = {"env": self.env()}
        if self.user is not None:
            args.update(user=self.uid, group=self.gid, extra_groups=[])
        return args

    def own(self, *paths):
        """Hands paths (whole trees) to the downloader user, never following links."""
        if self.user is None:
            return
        for top in paths:
            os.lchown(top, self.uid, self.gid)
            for dirpath, dirnames, filenames in os.walk(top, followlinks=False):
                for name in dirnames + filenames:
                    os.lchown(os.path.join(dirpath, name), self.uid, self.gid)

    def private_dir(self):
        """A folder only the downloader user can read (for a cookies file)."""
        path = tempfile.mkdtemp(prefix="modalfs-dl-")
        os.chmod(path, 0o700)
        self.own(path)
        return path

    def kill_all(self):
        """Ends every process left running as the downloader user: one that
        left its process group must not still be writing to work/ while the
        agent moves the files out of it."""
        if self.user is None:
            return
        for _ in range(3):
            found = False
            for pid in os.listdir("/proc"):
                if not pid.isdigit():
                    continue
                try:
                    if os.stat(f"/proc/{pid}").st_uid == self.uid:
                        os.kill(int(pid), signal.SIGKILL)
                        found = True
                except (FileNotFoundError, ProcessLookupError):
                    pass
            if not found:
                return
            time.sleep(0.2)

    def open_untrusted(self, path):
        """Opens a file the downloader user may have written (its logs), for
        reading by the agent: a regular file only, not through a link, and,
        when it runs as its own user, one that user owns. A link or hard link
        to a file of the agent's (/proc/1/environ, say) would otherwise end
        up in the job's log on the volume."""
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or (self.user is not None and (st.st_uid != self.uid or st.st_nlink != 1)):
            os.close(fd)
            raise PermissionError(f"{path} isn't a file the downloader wrote")
        return os.fdopen(fd, "rb")

    def check(self):
        """Fails the run if the downloader user can reach Modal's API after
        all: better no download than one that silently runs with the keys to
        the workspace (if Modal ever changes how containers authenticate).
        With WARP, also that nothing the downloader sends can go anywhere but
        the proxy (NetNS.check)."""
        if self.netns is not None:
            self.netns.check()
        if self.user is None or not os.path.exists("/run/modal.sock"):
            return
        probe = ("import socket\ns = socket.socket(socket.AF_UNIX)\n"
                 "try:\n    s.connect('/run/modal.sock')\n    print('reachable')\n"
                 "except OSError as e:\n    print('blocked', e.errno)\n")
        r = subprocess.run([sys.executable, "-I", "-c", probe], capture_output=True, text=True, timeout=30,
                           cwd="/", **self.popen_args())
        if not r.stdout.startswith("blocked"):
            raise RuntimeError("the downloader user can reach Modal's API (/run/modal.sock); refusing to run "
                               f"third-party downloaders with it: {r.stdout.strip()} {r.stderr.strip()[-200:]}")


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


def start_process(argv, log_path, sandbox, cwd=None):
    # Own process group: Modal's SIGINT goes to the agent, which pauses the
    # tool through its API instead of letting the signal kill it mid-write.
    # The log is opened here, by the agent, outside anything the tool can write.
    with open(log_path, "ab") as log:
        return subprocess.Popen(
            argv,
            cwd=cwd or sandbox.home,
            start_new_session=True,
            stdin=subprocess.DEVNULL,
            stdout=log,
            stderr=subprocess.STDOUT,
            **sandbox.popen_args(),
        )


# ---------------------------------------------------------------- WARP

# Run by `unshare -Urn` as the downloader user: a network namespace of its
# own with nothing but loopback, so whatever the downloader sends goes
# through the WARP proxy or nowhere. Inside, 127.0.0.1:$port leads to the
# proxy's unix socket; and, for a downloader with a control API (gopeed,
# aria2), $ctl leads back to it from the agent's side.
NS_SCRIPT = r'''
sock=$1; port=$2; ctl=$3; api=$4; shift 4
ip link set lo up || { echo "modalfs: no loopback in the downloader's network namespace" >&2; exit 97; }
socat "TCP-LISTEN:$port,bind=127.0.0.1,fork,reuseaddr" "UNIX-CONNECT:$sock" 2>/dev/null &
if [ -n "$ctl" ]; then socat "UNIX-LISTEN:$ctl,fork,unlink-early,mode=600" "TCP:127.0.0.1:$api" 2>/dev/null & fi
sleep 0.5
exec "$@"
'''

# What NetNS.check runs inside a namespace like the downloader's: every way
# out must fail.
LEAK_PROBE = r'''
import json, socket
out = {}
def tcp():
    socket.create_connection(("1.1.1.1", 443), timeout=4).close()
    return "connected to 1.1.1.1:443"
def udp():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(4)
    s.sendto(b"\0" * 12, ("1.1.1.1", 53))
    s.recvfrom(512)
    return "an answer from 1.1.1.1:53"
def dns():
    return socket.getaddrinfo("www.cloudflare.com", 443)[0][4][0]
for name, fn in (("tcp", tcp), ("udp", udp), ("dns", dns)):
    try:
        out[name] = "allowed: " + fn()
    except Exception as e:
        out[name] = f"blocked: {type(e).__name__}: {e}"
print(json.dumps(out))
'''


class NetNS:
    """Keeps a downloader to the WARP proxy: it runs in a network namespace
    of its own (NS_SCRIPT), where the only way out is the proxy's socket.
    Without iptables (gVisor has none) this is what makes "nothing goes
    direct" true by construction rather than by each tool's proxy setting:
    gopeed's BitTorrent engine, for one, ignores its proxy for peers."""

    def __init__(self, proxy_sock, sandbox):
        self.proxy_sock, self.sandbox = proxy_sock, sandbox
        self.helpers = []
        self.result = {}

    def wrap(self, argv, api_port=None):
        ctl, host_port = "", api_port
        if api_port:
            d = tempfile.mkdtemp(prefix="modalfs-ns-")
            os.chmod(d, 0o700)
            self.sandbox.own(d)
            ctl = os.path.join(d, "api.sock")
            host_port = free_port()
            self.helpers.append(subprocess.Popen(
                ["socat", f"TCP-LISTEN:{host_port},bind=127.0.0.1,fork,reuseaddr", f"UNIX-CONNECT:{ctl}"],
                start_new_session=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL))
        return ["unshare", "-Urn", "--", "sh", "-c", NS_SCRIPT, "modalfs-ns", self.proxy_sock, str(PROXY_PORT),
                ctl, str(api_port or ""), *argv], host_port

    def check(self):
        """Fails the run unless a namespace like the downloader's has no way
        out: no TCP, no UDP, no DNS."""
        why = f"{WARP_ERROR}: couldn't give the downloader a network of its own, so it can't be kept to WARP"
        try:
            r = subprocess.run(["unshare", "-Urn", "--", "sh", "-c", 'ip link set lo up && exec python3 -I -c "$0"',
                                LEAK_PROBE], capture_output=True, text=True, timeout=60, cwd="/",
                               **self.sandbox.popen_args())
        except (OSError, subprocess.TimeoutExpired) as e:
            raise Stop("failed", f"{why} ({e})")
        try:
            self.result = json.loads(r.stdout.strip().splitlines()[-1])
        except (ValueError, IndexError):
            raise Stop("failed", f"{why} ({(r.stderr or r.stdout).strip()[-300:] or f'exit {r.returncode}'})")
        leaks = {k: v for k, v in self.result.items() if not v.startswith("blocked")}
        if leaks:
            raise Stop("failed", f"{WARP_ERROR}: the downloader could still reach the internet directly "
                                 f"({json.dumps(leaks)}); refusing to download")

    def stop(self):
        for p in self.helpers:
            stop_process(p, signal.SIGTERM, 5)
        self.helpers = []


class Warp:
    """Cloudflare WARP for one run: an identity (registered once per volume
    with wgcf, kept in .modalfs/warp/, readable by root alone), and the
    proxy in warp/, run as root so the downloader can't touch it, serving a
    unix socket only the downloader's group may connect to.

    Never a silent fallback: if WARP can't be reached, the run fails with a
    message that starts with WARP_ERROR, and modalfs offers to retry without
    it; the user decides."""

    def __init__(self, root, job, env, log, sandbox):
        self.dir = os.path.join(root, ".modalfs", "warp")
        self.job, self.env, self.log, self.sandbox = job, env, log, sandbox
        self.profile = os.path.join(self.dir, "wgcf-profile.conf")
        self.sock_dir = tempfile.mkdtemp(prefix="modalfs-warp-")
        os.chmod(self.sock_dir, 0o755)
        self.sock = os.path.join(self.sock_dir, "proxy.sock")
        self.proc = None

    def env_vars(self):
        env = {k: os.environ[k] for k in ("PATH", "SSL_CERT_FILE", "SSL_CERT_DIR") if k in os.environ}
        env.setdefault("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
        env.update(HOME=self.dir, LANG="C.UTF-8")
        return env

    def start(self):
        """Connects, registering an identity first if there's none (or the
        one there stopped working). Returns what Cloudflare's trace page
        says through the tunnel: warp, colo, ip."""
        os.makedirs(self.dir, exist_ok=True)
        os.chmod(self.dir, 0o700)
        fresh = False
        if not os.path.exists(self.profile):
            self.register()
            fresh = True
        info, err = self.launch()
        if info is None and not fresh:
            # an identity Cloudflare no longer takes: a new one, once a run
            self.register()
            info, err = self.launch()
        if info is None:
            raise Stop("failed", f"{WARP_ERROR}: couldn't connect ({err})")
        return info

    def register(self):
        wgcf = self.env.binary("wgcf")
        for name in ("wgcf-account.toml", "wgcf-profile.conf"):
            with contextlib.suppress(FileNotFoundError):
                os.remove(os.path.join(self.dir, name))
        for args in (["register", "--accept-tos"], ["generate"]):
            try:
                r = subprocess.run([wgcf, *args], cwd=self.dir, env=self.env_vars(), capture_output=True, text=True,
                                   timeout=90)
            except (OSError, subprocess.TimeoutExpired) as e:
                raise Stop("failed", f"{WARP_ERROR}: couldn't register with Cloudflare ({e})")
            out = (r.stdout + r.stderr).strip()
            if r.returncode != 0:
                if "429" in out or "too many" in out.lower():
                    raise Stop("failed", f"{WARP_ERROR}: Cloudflare refused a new registration for now (too many "
                                         "from this network); try again later, or retry without WARP")
                raise Stop("failed", f"{WARP_ERROR}: couldn't register with Cloudflare: {out[-300:]}")
        for name in os.listdir(self.dir):
            os.chmod(os.path.join(self.dir, name), 0o600)
        self.env.sync()

    def launch(self):
        argv = [self.env.binary("warp"), "-profile", self.profile, "-listen", self.sock,
                "-group", str(self.sandbox.gid if self.sandbox.user is not None else -1)]
        if self.job.get("warp_endpoint"):  # tests: a WARP that can't be reached
            argv += ["-endpoint", self.job["warp_endpoint"]]
        self.stop()
        try:
            with open(self.log, "ab") as log:
                self.proc = subprocess.Popen(argv, cwd=self.dir, env=self.env_vars(), start_new_session=True,
                                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=log)
        except OSError as e:
            return None, str(e)
        line = []
        reader = threading.Thread(target=lambda: line.append(self.proc.stdout.readline()), daemon=True)
        reader.start()
        reader.join(150)  # it tries a few endpoints, about 20 s each
        try:
            ready = json.loads(line[0]) if line and line[0] else {}
        except ValueError:
            ready = {}
        if ready.get("ready"):
            return {k: v for k, v in ready.items() if k in ("warp", "colo", "ip", "loc", "endpoint")}, None
        self.stop()
        return None, ready.get("error") or "the proxy didn't start: " + " | ".join(tail(self.log, 3))

    def stop(self):
        stop_process(self.proc, signal.SIGTERM, 5)
        self.proc = None


def is_bittorrent(url):
    u = url.lower().split("?")[0]
    return u.startswith("magnet:") or u.endswith(".torrent")


# ---------------------------------------------------------------- tools


class Tool:
    """One downloader. start() picks up whatever earlier runs left in work/
    and state/; poll() reports progress; pause() leaves everything resumable."""

    name = ""

    def __init__(self, job, work, state, log, sandbox):
        # state/ is the agent's; what the tool itself writes goes in work/
        # and, for gopeed, state/data/, which belong to the downloader user
        self.job, self.work, self.state, self.log, self.sandbox = job, work, state, log, sandbox
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

    def __init__(self, job, work, state, log, sandbox, binary="gopeed"):
        super().__init__(job, work, state, log, sandbox)
        self.binary = binary
        self.proc = None
        self.task_file = os.path.join(state, "task-id")
        self.data = os.path.join(state, "data")  # gopeed's database and logs
        if not os.path.isdir(self.data):
            # an older agent kept the database in state/ itself
            os.makedirs(self.data)
            for name in os.listdir(state):
                if name not in ("task-id", "data"):
                    os.rename(os.path.join(state, name), os.path.join(self.data, name))

    def api(self, method, path, body=None):
        r = http_json(method, f"{self.base}{path}", body, {"X-Api-Token": self.token})
        if r.get("code", 0) != 0:
            raise RuntimeError(f"gopeed {method} {path}: {r.get('msg')}")
        return r.get("data")

    def start(self):
        self.token = secrets.token_hex(16)
        port = free_port()
        # The web build insists on a username and password whenever an API
        # token is set; nothing but the agent can reach it anyway.
        argv = [self.binary, "-A", "127.0.0.1", "-P", str(port), "-T", self.token,
                "-u", "modalfs", "-p", secrets.token_hex(16), "-d", self.data]
        argv, port = self.sandbox.wrap(argv, port)
        self.base = f"http://127.0.0.1:{port}"
        self.sandbox.own(self.work, self.data)
        self.proc = start_process(argv, self.log, self.sandbox, cwd=self.data)
        if not wait_until(self.alive, 30):
            raise RuntimeError("gopeed didn't start: " + " | ".join(tail(self.log, 5)))
        cfg = self.api("GET", "/api/v1/config")
        bt = cfg.setdefault("protocolConfig", {}).setdefault("bt", {})
        bt.update(seedKeep=False, seedRatio=0, seedTime=0)
        cfg["autoStartTasks"] = False
        if self.sandbox.netns is not None:
            cfg["proxy"] = {"enable": True, "system": False, "scheme": "socks5", "host": f"127.0.0.1:{PROXY_PORT}",
                            "usr": "", "pwd": ""}
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
            clear_dir(self.work, self.sandbox)
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
        # a finished task reports its size as its speed
        speed = 0 if state == "done" else p.get("speed", 0)
        return Progress(state, p.get("downloaded", 0), total, speed, t.get("name", ""),
                        error=self.last_error() if state == "error" else "")

    def logs(self):
        # with a storage dir, gopeed logs to <storage>/logs/core.log, not stdout
        return [self.log, os.path.join(self.data, "logs", "core.log")]

    def last_error(self):
        core = tail(self.logs()[1], 200, self.sandbox.open_untrusted)
        for line in reversed(core + tail(self.log, 200)):
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
        clear_dir(self.work, self.sandbox)

    def stop(self):
        stop_process(self.proc)


class Aria2(Tool):
    name = "aria2"

    def __init__(self, job, work, state, log, sandbox, binary="aria2c"):
        super().__init__(job, work, state, log, sandbox)
        self.binary = binary
        self.proc = None

    def rpc(self, method, *params):
        r = http_json("POST", f"http://127.0.0.1:{self.api_port}/jsonrpc",
                      {"jsonrpc": "2.0", "id": "m", "method": method, "params": [f"token:{self.secret}", *params]})
        return r["result"]

    def start(self):
        self.port, self.secret = free_port(), secrets.token_hex(16)
        self.api_port = self.port
        argv = [self.binary, "--enable-rpc", f"--rpc-listen-port={self.port}", "--rpc-listen-all=false",
                f"--rpc-secret={self.secret}", f"--dir={self.work}", "--continue=true",
                "--max-connection-per-server=16", "--split=16", "--min-split-size=4M",
                "--file-allocation=none", "--auto-save-interval=1", "--max-tries=0", "--retry-wait=5",
                "--allow-overwrite=false", "--auto-file-renaming=false", "--seed-time=0",
                "--bt-save-metadata=true", "--console-log-level=warn", "--summary-interval=0"]
        if self.job.get("limit"):
            argv.append(f"--max-overall-download-limit={self.job['limit']}")
        if self.sandbox.netns is not None:
            # aria2 speaks only HTTP proxies; CONNECT for plain HTTP too
            argv += [f"--all-proxy=http://127.0.0.1:{PROXY_PORT}", "--proxy-method=tunnel"]
        argv, self.api_port = self.sandbox.wrap(argv, self.port)
        self.sandbox.own(self.work)
        self.proc = start_process(argv, self.log, self.sandbox)
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

    def __init__(self, job, work, state, log, sandbox, binary="yt-dlp"):
        super().__init__(job, work, state, log, sandbox)
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
        if self.job.get("format"):  # the live test asks for a small one
            argv += ["-f", self.job["format"]]
        if self.job.get("cookies"):
            # never on the volume; readable by the downloader user alone, and
            # gone when this run of yt-dlp ends (stop() and pause())
            self.cookie_dir = self.sandbox.private_dir()
            path = os.path.join(self.cookie_dir, "cookies.txt")
            write_file(path, self.job["cookies"])
            os.chmod(path, 0o600)
            self.sandbox.own(path)
            argv += ["--cookies", path]
        if self.sandbox.netns is not None:
            # socks5h: names are looked up through the tunnel (there's no DNS
            # in the namespace); native downloads, because ffmpeg, which
            # yt-dlp otherwise hands some formats to, has no SOCKS support
            argv += ["--proxy", f"socks5h://127.0.0.1:{PROXY_PORT}", "--downloader", "native"]
        argv, _ = self.sandbox.wrap(argv)
        self.sandbox.own(self.work)
        self.proc = subprocess.Popen(argv, cwd=self.sandbox.home, start_new_session=True, stdin=subprocess.DEVNULL,
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                                     **self.sandbox.popen_args())
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
        if getattr(self, "cookie_dir", None):
            shutil.rmtree(self.cookie_dir, ignore_errors=True)
            self.cookie_dir = None

    def resume(self):
        self.paused = False
        self.run()

    def retry(self):
        self.resume()
        return True

    def stop(self):
        self.pause()


TOOLS = {"gopeed": Gopeed, "aria2": Aria2, "yt-dlp": YtDlp}


def clear_dir(path, sandbox):
    shutil.rmtree(path, ignore_errors=True)  # never follows links
    os.makedirs(path, exist_ok=True)
    sandbox.own(path)


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
        self.recent = []  # [t, done] over the last SPEED_WINDOW, to measure the speed
        self.speed = 0
        self.changed = threading.Condition(self.lock)
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
            self.status.update(fields, t=round(now(), 2))
            self.changed.notify_all()

    def sample(self, p: Progress):
        """Records a poll of the downloader, 4 times a second. The speed is
        what arrived over the last SPEED_WINDOW: steady enough to read, and
        measured the same way for every downloader (gopeed and yt-dlp report
        theirs once a second or in jumps). Returns that speed."""
        t = now()
        with self.lock:
            if p.state == "error" and not p.done:
                # a downloader that exited reports nothing: keep what it had
                p.done, p.total = self.status["done"], self.status["total"]
            # Only polls that saw the count change are kept: gopeed updates
            # it every 350 ms or so, and measuring between polls that fall
            # either side of an update would make the speed jitter.
            recent = self.recent
            if recent and p.done < recent[-1][1]:
                recent.clear()  # yt-dlp moved on to the next file
            if not recent or p.done != recent[-1][1]:
                recent.append([t, p.done])
            while len(recent) > 2 and recent[1][0] <= t - SPEED_WINDOW:
                recent.pop(0)
            first, last = recent[0], recent[-1]
            if t - last[0] > 1.0:
                last = [t, p.done]  # nothing new for a while: the speed falls
            span = last[0] - first[0]
            if p.state != "running" or t - recent[-1][0] >= SPEED_WINDOW:
                speed = 0
            elif span >= 0.5:
                speed = int((last[1] - first[1]) / span)
            else:
                speed = p.speed  # just started: the downloader's own figure
            # a light exponential average on top takes out what's left of
            # the jitter without making the figure lag much (about 0.6 s)
            if speed and self.speed:
                speed = int(self.speed + SPEED_SMOOTHING * (speed - self.speed))
            self.speed = speed
            eta = int((p.total - p.done) / speed) if speed > 0 and p.total > 0 else None
            t = round(t, 2)
            if not self.history or t - self.history[-1][0] >= HISTORY_EVERY - SAMPLE_EVERY / 2:
                self.history.append([round(t, 1), p.done, speed])
            self.status.update(done=p.done, total=p.total, speed=speed, eta=eta, t=t)
            if p.name:
                self.status["name"] = p.name
            if p.note:
                self.status["note"] = p.note
            self.changed.notify_all()
        return speed

    def wait_newer(self, t, timeout):
        """The status once it's newer than t, or as it is after timeout."""
        with self.changed:
            self.changed.wait_for(lambda: self.status["t"] != t, timeout)
            return dict(self.status)

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
    """GET /live: server-sent events, each sample as it's taken (4 a second). GET /history: this run's
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
                    s = record.snapshot()
                    while True:
                        self.wfile.write(f"data: {json.dumps(s)}\n\n".encode())
                        self.wfile.flush()
                        if s["state"] not in ("starting", "running", "retrying", "pausing", "finishing"):
                            return
                        # each new sample as it's taken; the same one again
                        # after a second keeps the connection known to be alive
                        s = record.wait_newer(s["t"], 1.0)
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
        env = {"gopeed": "GOPEED_BIN", "aria2": "ARIA2C_BIN", "yt-dlp": "YTDLP_BIN", "warp": "MODALFS_WARP_BIN",
               "wgcf": "WGCF_BIN"}[tool]
        return os.environ.get(env) or {"aria2": "aria2c", "warp": "modalfs-warp"}.get(tool, tool)

    def task_id(self):
        return f"local-{os.getpid()}"

    def after_signal(self):
        pass

    def spawn_successor(self, job):
        raise RuntimeError("continuing in a new Function is only possible on Modal")

    def sandbox(self):
        # the tests run the agent as root in a Linux container with this set
        name = os.environ.get("MODALFS_DOWNLOADER_USER")
        if name and os.geteuid() == 0:
            return Sandbox(pwd.getpwnam(name))
        return Sandbox()


class Agent:
    def __init__(self, job, root, env):
        self.job, self.root, self.env = job, root, env
        self.sandbox = env.sandbox()
        self.jobdir = os.path.join(root, ".modalfs", "downloads", job["id"])
        self.work = os.path.join(self.jobdir, "work")
        self.state = os.path.join(self.jobdir, "state")
        # off the volume while the tools write to it; copied to log.txt at the end
        self.log = os.path.join(tempfile.mkdtemp(prefix="modalfs-agent-"), "tools.log")
        self.signalled = threading.Event()
        self.sampling_done = threading.Event()
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
        return cls(self.job, self.work, os.path.join(self.state, name), self.log, self.sandbox,
                   binary=self.env.binary(name))

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
        backoff_until = 0.0  # after continuing a failed download, errors wait until then
        stall_limit = float(job.get("stall_limit", 600))
        kick_after = float(job.get("kick_after", KICK_AFTER))
        try:
            if job.get("warp"):
                self.connect_warp()
            self.sandbox.check()
            if self.sandbox.netns is not None:
                rec.event("warp", leak_check=self.sandbox.netns.result, **self.warp_info)
                rec.update(warp=self.warp_info, note="")
            tool.start()
            rec.update(state="running", tool=name)
            self.sampler = threading.Thread(target=self.sample_loop, daemon=True)
            self.sampler.start()
            while True:
                p = tool.poll()
                speed = rec.speed
                if p.state == "done":
                    self.stop_sampling()
                    rec.sample(p)
                    return self.finish(tool)
                if self.signalled.is_set():
                    return self.save_and_end(tool, "interrupted")
                if self.pause_requested.is_set():
                    return self.save_and_end(tool, "paused")
                if now() >= self.deadline:
                    return self.time_limit(tool)
                if p.state == "error" and now() < backoff_until:
                    time.sleep(SAMPLE_EVERY)
                    continue
                if p.state == "error":
                    retries += 1
                    done_at_error = p.done
                    rec.event("error", tool=tool.name, message=p.error, retry=retries)
                    if retries <= RETRIES and tool.retry():
                        rec.update(state="retrying", error=p.error)
                        backoff_until = now() + 2 * retries
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
                if p.state == "running" and speed == 0:
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
                time.sleep(SAMPLE_EVERY)
        except Stop as e:
            return self.end(tool, e.state, e.message)
        except Exception as e:  # report it on the volume, then let Modal see the failure
            self.end(tool, "failed", f"{type(e).__name__}: {e}")
            raise

    def connect_warp(self):
        """Starts WARP and puts the downloaders in a namespace where it's
        their only way out (NetNS); Sandbox.check then proves it."""
        if is_bittorrent(self.job["url"]):
            raise Stop("failed", f"{WARP_ERROR} can't carry BitTorrent: its peers would be reached directly. "
                                 "Add the link again without WARP.")
        self.record.update(state="starting", note="connecting to Cloudflare WARP")
        self.warp = Warp(self.root, self.job, self.env, self.log, self.sandbox)
        self.warp_info = self.warp.start()
        self.sandbox.netns = NetNS(self.warp.sock, self.sandbox)

    def stop_warp(self):
        if self.sandbox.netns is not None:
            self.sandbox.netns.stop()
        warp = getattr(self, "warp", None)
        if warp is not None:
            warp.stop()
            shutil.rmtree(warp.sock_dir, ignore_errors=True)

    def sample_loop(self):
        """Samples the downloader 4 times a second for the record and the
        live view, apart from the loop that acts on what it sees: continuing
        a failed download, or writing an event (a `sync`), can take seconds."""
        while not self.sampling_done.wait(SAMPLE_EVERY):
            try:
                self.record.sample(self.tool.poll())
            except Exception:  # between downloaders, or one stopping
                pass

    def stop_sampling(self):
        """Stops the sampler before the downloader stops: a stopped one has
        nothing to report."""
        self.sampling_done.set()
        sampler = getattr(self, "sampler", None)
        if sampler and sampler is not threading.current_thread():
            sampler.join(10)

    def save_and_end(self, tool, state):
        self.stop_sampling()
        self.record.update(state="pausing")
        self.pause_tool(tool)
        self.record.event("signal" if state == "interrupted" else "paused", done=self.record.snapshot()["done"])
        summary = self.end(None, state)
        if state == "interrupted":
            self.env.after_signal()
        return summary

    def time_limit(self, tool):
        self.stop_sampling()
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
            self.record.sample(tool.poll())  # where it paused
        except Exception as e:  # its saved state is whatever it last wrote
            self.record.event("error", message=f"couldn't pause {tool.name} cleanly: {e}")
        tool.stop()

    def end(self, tool, state, message="", **fields):
        self.stop_sampling()
        if tool is not None:
            with contextlib.suppress(Exception):
                tool.stop()
        self.sandbox.kill_all()
        self.stop_warp()
        if state in ("failed", "stalled"):
            fields["log_tail"] = [line for path in self.logs() for line in tail(path, 25, self.opener(path))]
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

    def opener(self, path):
        """How the agent opens a log: its own as is, one the downloader
        wrote only if it's what it seems (Sandbox.open_untrusted)."""
        return (lambda p: open(p, "rb")) if path == self.log else self.sandbox.open_untrusted

    def copy_log(self):
        # the logs live outside the volume while the tool runs (/tmp in
        # Modal, or its state folder, removed when the download finishes)
        with open(os.path.join(self.jobdir, "log.txt"), "ab") as out:
            for path in self.logs():
                with contextlib.suppress(OSError), self.opener(path)(path) as f:
                    out.write(f"--- {os.path.basename(path)}\n".encode())
                    shutil.copyfileobj(f, out)

    def finish(self, tool):
        tool.stop()
        # nothing of the downloader's may still run while its files are moved
        self.sandbox.kill_all()
        self.record.update(state="finishing")
        root = os.path.realpath(self.root)
        dest = safe_join(root, self.job.get("folder", ""))
        os.makedirs(dest, exist_ok=True)
        moved = []
        for src in sorted(downloaded_files(self.work)):
            rel = os.path.relpath(src, self.work)
            target = unique_path(safe_join(dest, rel))
            os.makedirs(os.path.dirname(target), exist_ok=True)
            os.rename(src, target)  # src itself is a plain file (downloaded_files)
            if self.sandbox.user is not None:
                os.lchown(target, os.getuid(), os.getgid())
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
    """The finished files in work/: plain files only. A link, or a hard link
    to a file elsewhere on the volume, is never moved into the destination;
    os.walk doesn't enter linked folders."""
    for dirpath, dirnames, filenames in os.walk(work, followlinks=False):
        for f in filenames:
            path = os.path.join(dirpath, f)
            if is_control_file(f):
                continue
            st = os.lstat(path)
            if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1:
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


# What the live test runs as the downloader user, inside a real download
# Function (job["selftest"] == "isolation"): everything it tries must fail.
# It's given the agent's MODAL_* variables, the worst case of a downloader
# that learned them somehow; the socket is what it lacks.
ISOLATION_PROBE = r'''
import glob, json, os, socket, sys, threading
other, app, jobdir = sys.argv[1:4]
out = {}
def attempt(name, fn, timeout=20):
    res = {}
    def run():
        try:
            res["allowed"] = repr(fn())[:200]
        except BaseException as e:
            res["blocked"] = f"{type(e).__name__}: {e}"[:300]
    t = threading.Thread(target=run, daemon=True)
    t.start()
    t.join(timeout)
    out[name] = res or {"blocked": f"no answer in {timeout} s"}
def connect():
    s = socket.socket(socket.AF_UNIX)
    s.connect("/run/modal.sock")
    return "connected"
def write(path):
    with open(path, "w") as f:
        f.write("x")
    return path
attempt("connect to /run/modal.sock", connect)
attempt("read /proc/1/environ", lambda: open("/proc/1/environ", "rb").read(64))
attempt("write the volume's root", lambda: write("/vol/modalfs-probe"))
attempt("write the agent's record", lambda: write(os.path.join(jobdir, "status.json")))
import modal
attempt("list volumes", lambda: [v.name for v in modal.Volume.objects.list()][:3])
attempt("read another volume", lambda: modal.Volume.from_name(other).listdir("/"))
def upload():
    with modal.Volume.from_name(other).batch_upload() as b:
        b.put_file(__file__ if os.path.exists(__file__) else "/etc/hostname", "/probe")
    return "uploaded"
attempt("write another volume", upload)
attempt("delete another volume", lambda: modal.Volume.objects.delete(other))
attempt("spawn a Function", lambda: modal.Function.from_name(app, "download").spawn({"selftest": "noop"}).object_id)
def sandbox():
    sb = modal.Sandbox.create("true", app=modal.App.lookup(app))
    sb.terminate()
    return sb.object_id
attempt("create a Sandbox", sandbox)
readable = {}
for pattern in ("/proc/[0-9]*/environ", "/proc/[0-9]*/cmdline", "/tmp/**", "/home/**", "/run/**", "/__modal/**"):
    for p in glob.glob(pattern, recursive=True):
        if p.startswith("/__modal/volumes") or not os.path.isfile(p):
            continue
        try:
            with open(p, "rb") as f:
                readable[p] = f.read(1 << 16).decode("utf-8", "replace")
        except OSError:
            pass
print(json.dumps({"attempts": out, "readable": readable}))
'''


def selftest(job, root, env):
    if job["selftest"] == "noop":
        return {"selftest": "noop"}
    if job["selftest"] != "isolation":
        raise RuntimeError(f"unknown self-test {job['selftest']!r}")
    agent = Agent(job, root, env)
    sb = agent.sandbox
    os.makedirs(agent.jobdir, exist_ok=True)
    write_file(os.path.join(agent.jobdir, "status.json"), "{}")
    args = sb.popen_args()
    args["env"].update({k: v for k, v in os.environ.items() if k.startswith("MODAL_")})
    # and Modal's client, which the Function's own Python has on its path
    args["env"]["PYTHONPATH"] = os.pathsep.join(p for p in sys.path if p)
    r = subprocess.run([sys.executable, "-c", ISOLATION_PROBE, job.get("other_volume", ""), job.get("app", APP_NAME),
                        agent.jobdir], capture_output=True, text=True, timeout=600, cwd=sb.home, **args)
    try:
        found = json.loads(r.stdout.strip().splitlines()[-1])
    except (ValueError, IndexError):
        return {"selftest": "isolation", "error": r.stderr[-2000:], "user": sb.uid}
    secrets_seen = [p for p, text in found["readable"].items()
                    if job["live_token"] in text or (job.get("cookies") and job["cookies"] in text)]
    # The control: the same API call as the agent, which must work (so the
    # timeouts above are the downloader user being shut out, not a slow API).
    t = now()
    try:
        control = f"root listed {len(list(modal.Volume.objects.list()))} volumes in {now() - t:.1f} s"
    except Exception as e:
        control = f"root couldn't list volumes either: {e}"
    return {"selftest": "isolation", "user": sb.uid, "control": control, "attempts": found["attempts"],
            "secrets_readable": secrets_seen, "readable_files": len(found["readable"])}


def run_job(job, root, env):
    if job.get("selftest"):
        return selftest(job, root, env)
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
    WARP_SRC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "warp")  # modalfs deploys it next to app.py
    image = (
        modal.Image.debian_slim(python_version="3.12")
        # socat, iproute2 and util-linux's unshare: the downloader's own network (NetNS)
        .apt_install("aria2", "ffmpeg", "curl", "unzip", "ca-certificates", "socat", "iproute2", "util-linux")
        .run_commands(
            # the user the downloaders run as (Sandbox)
            f"useradd --system --create-home --home-dir /home/{DOWNLOADER_USER} --shell /usr/sbin/nologin {DOWNLOADER_USER}",
            f"curl -fsSL -o /tmp/g.zip https://github.com/GopeedLab/gopeed/releases/download/{GOPEED_VERSION}/gopeed-web-{GOPEED_VERSION}-linux-amd64.zip",
            f"echo '{GOPEED_SHA256}  /tmp/g.zip' | sha256sum -c -",
            "cd /tmp && unzip -q g.zip && mv gopeed-web-*/gopeed /usr/local/bin/gopeed && rm -rf /tmp/g.zip /tmp/gopeed-web-*",
            f"curl -fsSL -o /tmp/d.zip https://github.com/denoland/deno/releases/download/{DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip",
            f"echo '{DENO_SHA256}  /tmp/d.zip' | sha256sum -c -",
            "cd /tmp && unzip -q d.zip && mv deno /usr/local/bin/deno && rm /tmp/d.zip",
            f"curl -fsSL -o /usr/local/bin/wgcf https://github.com/ViRb3/wgcf/releases/download/{WGCF_VERSION}/wgcf_{WGCF_VERSION[1:]}_linux_amd64",
            f"echo '{WGCF_SHA256}  /usr/local/bin/wgcf' | sha256sum -c - && chmod 755 /usr/local/bin/wgcf",
        )
    )
    if modal.is_local():  # the source is only there when deploying
        image = image.add_local_dir(WARP_SRC, "/opt/modalfs-warp", copy=True)
    image = (
        image.run_commands(
            # The WARP proxy, built from source with a pinned Go; its
            # dependencies are checked against go.sum. Nothing of the build
            # stays in the image.
            f"curl -fsSL -o /tmp/go.tgz https://go.dev/dl/go{GO_VERSION}.linux-amd64.tar.gz",
            f"echo '{GO_SHA256}  /tmp/go.tgz' | sha256sum -c -",
            "tar -C /tmp -xzf /tmp/go.tgz && rm /tmp/go.tgz",
            "cd /opt/modalfs-warp && GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0 GOPATH=/tmp/gopath "
            "GOCACHE=/tmp/gocache /tmp/go/bin/go build -tags modalfs_warp -trimpath -ldflags='-s -w' "
            "-o /usr/local/bin/modalfs-warp . && rm -rf /tmp/go /tmp/gopath /tmp/gocache /opt/modalfs-warp",
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
            return {"aria2": "aria2c", "warp": "modalfs-warp"}.get(tool, tool)

        def sandbox(self):
            return Sandbox(pwd.getpwnam(DOWNLOADER_USER))

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
