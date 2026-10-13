"""Tests for the download agent, run outside Modal: a temp folder stands in
for the volume and a local server for the internet. They use the real
downloaders when they're installed (GOPEED_BIN, ARIA2C_BIN, or on PATH) and
skip otherwise; `just agent-tools` fetches gopeed.

    just test-agent
"""

import hashlib
import http.server
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import app  # noqa: E402

MiB = 1 << 20


def find(env, name):
    path = os.environ.get(env) or shutil.which(name)
    return path if path and os.path.exists(path) else None


GOPEED = find("GOPEED_BIN", "gopeed")
ARIA2 = find("ARIA2C_BIN", "aria2c")


class Origin:
    """A file server with Range support, a per-connection speed limit and
    ways to misbehave: stall after some bytes, or answer ranges with 200."""

    def __init__(self, size, rate=8 * MiB):
        self.data = os.urandom(size)
        self.sha = hashlib.sha256(self.data).hexdigest()
        self.rate = rate
        self.stall_after = None  # stop sending (but keep the connection) after this many bytes in total
        self.bad_range = None  # answer the ranged request with this number (1 = first) with 200 and the whole file
        self.ranged = 0
        self.served = 0
        self.lock = threading.Lock()
        origin = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_HEAD(self):
                self.send_response(200)
                self.send_header("Content-Length", str(len(origin.data)))
                self.send_header("Accept-Ranges", "bytes")
                self.end_headers()

            def do_GET(self):
                start, end = 0, len(origin.data) - 1
                rng = self.headers.get("Range")
                with origin.lock:
                    if rng:
                        origin.ranged += 1
                    bad = bool(rng) and origin.ranged == origin.bad_range
                if rng and not bad:
                    a, b = rng.removeprefix("bytes=").split("-")
                    start = int(a)
                    end = int(b) if b else end
                    self.send_response(206)
                    self.send_header("Content-Range", f"bytes {start}-{end}/{len(origin.data)}")
                else:
                    self.send_response(200)
                self.send_header("Content-Length", str(end - start + 1))
                self.send_header("Accept-Ranges", "bytes")
                self.end_headers()
                pos = start
                try:
                    while pos <= end:
                        with origin.lock:
                            if origin.stall_after is not None and origin.served >= origin.stall_after:
                                stalled = True
                            else:
                                stalled = False
                        if stalled:
                            time.sleep(0.2)
                            continue
                        n = min(64 * 1024, end + 1 - pos)
                        self.wfile.write(origin.data[pos:pos + n])
                        pos += n
                        with origin.lock:
                            origin.served += n
                        time.sleep(n / origin.rate)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def log_message(self, *args):
                pass

        class Server(http.server.ThreadingHTTPServer):
            daemon_threads = True

            def handle_error(self, request, client_address):
                pass  # downloaders drop connections all the time

        self.server = Server(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}/file.bin"

    def close(self):
        self.server.shutdown()


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="mfs-agent-")
        os.chmod(self.root, 0o755)  # like a volume: the downloader user (if any) can enter it
        self.addCleanup(shutil.rmtree, self.root, True)
        self.procs = []
        self.addCleanup(self.kill_all)

    def kill_all(self):
        for p in self.procs:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait()
            for pipe in (p.stdout, p.stderr):
                if pipe and not pipe.closed:
                    pipe.close()
        # downloaders started by a killed agent run in their own sessions
        subprocess.run(["pkill", "-9", "-f", self.root], check=False)

    def job(self, origin, **kw):
        job = {"id": "j1", "url": origin.url, "tool": "gopeed", "folder": "Downloads", "volume_id": "vo-test",
               "live_token": "secret-token", "connections": 4, "status_every": 0.3, "speed_chunk": 2}
        job.update(kw)
        return job

    def start(self, job):
        path = os.path.join(self.root, f"job-{len(self.procs)}.json")
        with open(path, "w") as f:
            json.dump(job, f)
        env = dict(os.environ)
        if GOPEED:
            env["GOPEED_BIN"] = GOPEED
        if ARIA2:
            env["ARIA2C_BIN"] = ARIA2
        p = subprocess.Popen([sys.executable, os.path.join(HERE, "app.py"), "run", path, "--root", self.root],
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env,
                             start_new_session=True)
        self.procs.append(p)
        return p

    def finish(self, p, timeout=120):
        out, err = p.communicate(timeout=timeout)
        self.assertEqual(p.returncode, 0, err[-2000:])
        return json.loads(out.strip().splitlines()[-1])

    def jobdir(self, jid="j1"):
        return os.path.join(self.root, ".modalfs", "downloads", jid)

    def status(self, jid="j1"):
        try:
            with open(os.path.join(self.jobdir(jid), "status.json")) as f:
                return json.load(f)
        except (OSError, ValueError):
            return {}

    def events(self, jid="j1"):
        with open(os.path.join(self.jobdir(jid), "events.jsonl")) as f:
            return [json.loads(line) for line in f]

    def wait_status(self, check, timeout=60):
        end = time.time() + timeout
        while time.time() < end:
            s = self.status()
            if s and check(s):
                return s
            time.sleep(0.1)
        self.fail(f"status never matched; last: {self.status()}")

    def check_file(self, origin, rel="Downloads/file.bin"):
        path = os.path.join(self.root, rel)
        with open(path, "rb") as f:
            self.assertEqual(hashlib.sha256(f.read()).hexdigest(), origin.sha, f"{rel} has the wrong content")

    def kill_and_resume(self, tool, rate):
        # kill -9 of everything, what a lost container looks like, then a new run
        origin = Origin(64 * MiB, rate=rate)
        self.addCleanup(origin.close)
        job = self.job(origin, tool=tool)
        self.start(job)
        self.wait_status(lambda s: s.get("done", 0) > 16 * MiB)
        time.sleep(1.5)  # aria2 saves its control file every second
        self.kill_all()
        summary = self.finish(self.start(job))
        self.assertEqual(summary["state"], "done")
        self.check_file(origin)
        self.assertLess(origin.served, len(origin.data) * 1.25,
                        f"{origin.served >> 20} MiB served for a 64 MiB file: it started over")
        self.assertEqual([e["ev"] for e in self.events()].count("start"), 2)

    def pause_and_resume(self, tool, rate):
        origin = Origin(64 * MiB, rate=rate)
        self.addCleanup(origin.close)
        job = self.job(origin, tool=tool)
        p = self.start(job)
        s = self.wait_status(lambda s: s.get("done", 0) > 16 * MiB and s.get("live_url"))
        req = urllib.request.Request(s["live_url"] + "/pause", method="POST")
        with self.assertRaises(urllib.error.HTTPError) as e:
            urllib.request.urlopen(req, timeout=5)
        self.assertEqual(e.exception.code, 403)
        e.exception.close()
        req.add_header("X-Modalfs-Token", job["live_token"])
        self.assertEqual(urllib.request.urlopen(req, timeout=5).status, 202)
        summary = self.finish(p)
        self.assertEqual(summary["state"], "paused")
        self.assertGreater(summary["done"], 16 * MiB)

        summary = self.finish(self.start(job))
        self.assertEqual(summary["state"], "done")
        self.check_file(origin)
        self.assertLess(origin.served, len(origin.data) * 1.25, "it started over after the pause")
        self.assertEqual([e["ev"] for e in self.events()].count("start"), 2)


@unittest.skipUnless(GOPEED, "gopeed isn't installed (just agent-tools)")
class GopeedTest(AgentTest):
    def test_downloads_into_the_folder(self):
        origin = Origin(24 * MiB)
        self.addCleanup(origin.close)
        summary = self.finish(self.start(self.job(origin)))
        self.assertEqual(summary["state"], "done")
        self.check_file(origin)
        self.assertEqual(summary["files"][0]["sha256"], origin.sha)
        kinds = [e["ev"] for e in self.events()]
        self.assertEqual(kinds[0], "start")
        self.assertIn("finished", kinds)
        chunks = os.listdir(os.path.join(self.jobdir(), "speed"))
        self.assertTrue(chunks, "no speed history was written")
        self.assertFalse(os.path.exists(os.path.join(self.jobdir(), "work")), "the staging folder was left behind")
        with open(os.path.join(self.jobdir(), "job.json")) as f:
            self.assertNotIn("live_token", json.load(f))

    def test_never_overwrites(self):
        origin = Origin(4 * MiB)
        self.addCleanup(origin.close)
        os.makedirs(os.path.join(self.root, "Downloads"))
        with open(os.path.join(self.root, "Downloads", "file.bin"), "w") as f:
            f.write("the user's own file")
        summary = self.finish(self.start(self.job(origin)))
        self.assertEqual(summary["files"][0]["path"], "Downloads/file (1).bin")
        with open(os.path.join(self.root, "Downloads", "file.bin")) as f:
            self.assertEqual(f.read(), "the user's own file")
        self.check_file(origin, "Downloads/file (1).bin")

    def test_resumes_after_kill(self):
        self.kill_and_resume("gopeed", rate=4 * MiB)
        lost = [e for e in self.events() if e["ev"] == "interrupted"]
        self.assertTrue(lost and lost[0]["warned"] is False, self.events())

    def test_pause_and_resume(self):
        self.pause_and_resume("gopeed", rate=4 * MiB)

    def test_live_stream(self):
        origin = Origin(32 * MiB, rate=4 * MiB)
        self.addCleanup(origin.close)
        job = self.job(origin)
        p = self.start(job)
        s = self.wait_status(lambda s: s.get("live_url") and s.get("state") == "running")
        req = urllib.request.Request(s["live_url"] + "/live", headers={"X-Modalfs-Token": job["live_token"]})
        times, t0 = [], time.time()
        with urllib.request.urlopen(req, timeout=10) as r:
            for line in r:
                if line.startswith(b"data: "):
                    times.append(json.loads(line[6:])["t"])
                if time.time() - t0 > 2:
                    break
        self.assertGreaterEqual(len(times), 6, "the live stream should send about 4 updates a second")
        self.assertEqual(len(set(times)), len(times), f"the live stream repeated a sample: {times}")
        self.finish(p)

    def test_stall_watchdog(self):
        origin = Origin(64 * MiB, rate=8 * MiB)
        origin.stall_after = 12 * MiB
        self.addCleanup(origin.close)
        summary = self.finish(self.start(self.job(origin, kick_after=2, stall_limit=6)))
        self.assertEqual(summary["state"], "stalled")
        actions = [e.get("action") for e in self.events() if e["ev"] == "stalled"]
        self.assertEqual(actions, ["reconnect", "stop"])

    def test_continues_after_range_ignored(self):
        # One connection gets 200 to a range request: gopeed fails the whole
        # task. The agent continues it (and falls back to aria2 if that fails).
        origin = Origin(48 * MiB, rate=4 * MiB)
        origin.bad_range = 4  # after the probe, on one of the download connections
        self.addCleanup(origin.close)
        summary = self.finish(self.start(self.job(origin, connections=8)))
        kinds = [e["ev"] for e in self.events()]
        if "error" not in kinds:
            self.skipTest("gopeed didn't hit the error this time")
        self.assertEqual(summary["state"], "done", kinds)
        self.check_file(origin)
        errors = [e.get("message") for e in self.events() if e["ev"] == "error"]
        print(f"\n  after a 200 to a range: {kinds} {errors}", file=sys.stderr)
        self.assertNotIn("gopeed reported an error without a message", errors)
        # waiting before continuing doesn't stop the sampling (the live view)
        rows = []
        speed = os.path.join(self.jobdir(), "speed")
        for name in sorted(os.listdir(speed)):
            with open(os.path.join(speed, name)) as f:
                rows += [json.loads(line) for line in f]
        gaps = [b[0] - a[0] for a, b in zip(rows, rows[1:])]
        self.assertLess(max(gaps), 2, f"the speed history has a {max(gaps):.1f} s gap")


@unittest.skipUnless(ARIA2, "aria2c isn't installed")
class Aria2Test(AgentTest):
    # aria2 opens 16 connections to the origin, so it gets a slower one.
    def test_resumes_after_kill(self):
        self.kill_and_resume("aria2", rate=256 * 1024)

    def test_pause_and_resume(self):
        self.pause_and_resume("aria2", rate=256 * 1024)

    def test_fallback_sticks(self):
        # After a fallback to aria2, later runs continue with aria2, which
        # has the partial download, even though the job asks for gopeed.
        origin = Origin(8 * MiB)
        self.addCleanup(origin.close)
        os.makedirs(os.path.join(self.jobdir(), "state"))
        with open(os.path.join(self.jobdir(), "state", "tool"), "w") as f:
            f.write("aria2")
        summary = self.finish(self.start(self.job(origin, tool="gopeed")))
        self.assertEqual(summary["state"], "done")
        self.assertEqual(self.status()["tool"], "aria2")
        self.check_file(origin)


class SpeedTest(unittest.TestCase):
    """The speed the agent shows, from a downloader that updates its count
    every 350 ms (as gopeed does) polled every 250 ms."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.clock, self.done, self.next_update = 1000.0, 0, 1000.0
        real = app.now
        app.now = lambda: self.clock
        self.addCleanup(setattr, app, "now", real)
        self.rec = app.Record(self.dir, app.LocalEnv(), {"id": "j1", "url": "https://x/y", "status_every": 3600})
        self.addCleanup(self.rec.closed.set)

    def poll(self, seconds, rate):
        """Polls for seconds while the count moves by rate (0: stalled)."""
        speeds = []
        for _ in range(int(seconds / 0.25)):
            self.clock += 0.25
            while rate and self.clock >= self.next_update:
                self.done += int(0.35 * rate)
                self.next_update += 0.35
            speeds.append(self.rec.sample(app.Progress("running", self.done, 1 << 40, 0)))
        return speeds

    def test_steady_and_stalled(self):
        rate = 50 * MiB
        speeds = self.poll(10, rate)
        for s in speeds[12:]:  # after 3 s
            self.assertAlmostEqual(s / rate, 1, delta=0.04, msg=f"speed {s / MiB:.1f} MiB/s for {rate / MiB:.0f}")
        stalled = self.poll(4, 0)
        self.assertEqual(stalled[-1], 0, "a stalled download should show no speed")
        rows = self.rec.history_since()
        self.assertTrue(13 <= len(rows) <= 15, f"{len(rows)} history rows for 14 s: one a second")
        status = self.rec.snapshot()
        self.assertEqual(status["speed"], 0)
        self.assertIsNone(status["eta"])


class PathTest(unittest.TestCase):
    def test_paths_stay_inside(self):
        base = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, base)
        self.assertEqual(app.safe_join(base, "a/b.bin"), os.path.join(os.path.realpath(base), "a", "b.bin"))
        for bad in ("../x", "a/../../x", "/../../etc/passwd"):
            with self.assertRaises(app.Stop):
                app.safe_join(base, bad)
        os.symlink("/etc", os.path.join(base, "link"))
        with self.assertRaises(app.Stop):
            app.safe_join(base, "link/passwd")

    def test_links_and_control_files_are_not_moved(self):
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        for name in ("movie.mp4", "movie.mp4.aria2", "x.part"):
            open(os.path.join(work, name), "w").close()
        os.symlink("/etc/passwd", os.path.join(work, "passwd"))
        self.assertEqual([os.path.basename(p) for p in app.downloaded_files(work)], ["movie.mp4"])

    def test_hard_links_are_not_moved(self):
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        elsewhere = os.path.join(work, "..", os.path.basename(work) + "-secret")
        open(elsewhere, "w").close()
        self.addCleanup(os.remove, elsewhere)
        os.link(elsewhere, os.path.join(work, "looks-downloaded.bin"))
        self.assertEqual(list(app.downloaded_files(work)), [])

    def test_unique_path(self):
        base = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, base)
        p = os.path.join(base, "a.txt")
        self.assertEqual(app.unique_path(p), p)
        open(p, "w").close()
        open(os.path.join(base, "a (1).txt"), "w").close()
        self.assertEqual(app.unique_path(p), os.path.join(base, "a (2).txt"))

    def test_strip_credentials(self):
        self.assertEqual(app.strip_credentials("https://u:p@host/a/b?sig=1#f"), "https://host/a/b")
        self.assertEqual(app.strip_credentials("magnet:?xt=urn:btih:abc&tr=https://t/passkey"), "magnet:?xt=urn:btih:abc")


class SandboxTest(unittest.TestCase):
    def test_environment_has_nothing_of_the_agents(self):
        os.environ["MODAL_TASK_ID"] = "ta-test"
        os.environ["MODALFS_SECRET"] = "x"
        self.addCleanup(os.environ.pop, "MODAL_TASK_ID")
        self.addCleanup(os.environ.pop, "MODALFS_SECRET")
        env = app.Sandbox().env()
        self.assertFalse([k for k in env if k.startswith(("MODAL", "MODALFS"))], env)
        self.assertIn("PATH", env)
        self.assertIn("HOME", env)

    def test_gopeed_database_moves_to_its_own_folder(self):
        # An older agent kept gopeed's database in state/gopeed itself; a
        # download it started must still resume.
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        state = os.path.join(d, "gopeed")
        os.makedirs(os.path.join(state, "logs"))
        for name in ("gopeed.db", "task-id"):
            open(os.path.join(state, name), "w").close()
        app.Gopeed({}, os.path.join(d, "work"), state, os.path.join(d, "log"), app.Sandbox())
        self.assertEqual(sorted(os.listdir(state)), ["data", "task-id"])
        self.assertEqual(sorted(os.listdir(os.path.join(state, "data"))), ["gopeed.db", "logs"])

    def test_logs_through_links_are_refused(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        target = os.path.join(d, "agent-secret")
        with open(target, "w") as f:
            f.write("secret")
        os.symlink(target, os.path.join(d, "core.log"))
        with self.assertRaises(OSError):
            app.Sandbox().open_untrusted(os.path.join(d, "core.log"))
        self.assertEqual(app.tail(os.path.join(d, "core.log"), opener=app.Sandbox().open_untrusted), [])


# The downloaders as their own user, as in Modal: Linux, as root, with
# MODALFS_DOWNLOADER_USER naming an unprivileged user (the CI and the Debian
# container the agent is tested in set it up).
DL_USER = os.environ.get("MODALFS_DOWNLOADER_USER")
AS_ROOT = sys.platform.startswith("linux") and os.geteuid() == 0 and DL_USER


@unittest.skipUnless(AS_ROOT, "needs root on Linux and MODALFS_DOWNLOADER_USER")
class DownloaderUserTest(AgentTest):
    def setUp(self):
        super().setUp()
        import pwd
        self.user = pwd.getpwnam(DL_USER)

    def test_runs_as_its_own_user(self):
        # A fake downloader that reports who it is and what it can see.
        probe = os.path.join(self.root, "whoami-downloader")
        with open(probe, "w") as f:
            f.write("#!/bin/sh\nid -u > /tmp/dl-probe; env >> /tmp/dl-probe\n"
                    "touch \"$4\"/../status.json 2>>/tmp/dl-probe || echo denied >> /tmp/dl-probe\n")
        os.chmod(probe, 0o755)
        sb = app.LocalEnv().sandbox()
        os.environ["MODAL_TASK_ID"] = "ta-secret"
        self.addCleanup(os.environ.pop, "MODAL_TASK_ID")
        work = os.path.join(self.root, "work")
        os.makedirs(work)
        sb.own(work)
        log = os.path.join(self.root, "log")
        p = app.start_process([probe, "a", "b", "c", work], log, sb)
        p.wait(10)
        with open("/tmp/dl-probe") as f:
            seen = f.read()
        os.remove("/tmp/dl-probe")
        self.assertTrue(seen.startswith(str(self.user.pw_uid)), seen)
        self.assertNotIn("MODAL_TASK_ID", seen)
        self.assertIn("denied", seen)
        self.assertEqual(os.stat(work).st_uid, self.user.pw_uid)

    @unittest.skipUnless(ARIA2, "aria2c isn't installed")
    def test_aria2_downloads_as_its_user(self):
        origin = Origin(8 * MiB)
        self.addCleanup(origin.close)
        summary = self.finish(self.start(self.job(origin, tool="aria2")))
        self.assertEqual(summary["state"], "done")
        self.check_file(origin)
        path = os.path.join(self.root, "Downloads", "file.bin")
        self.assertEqual(os.stat(path).st_uid, 0, "a finished file is the agent's, not the downloader's")
        self.assertEqual(os.stat(self.jobdir()).st_uid, 0)

    @unittest.skipUnless(GOPEED, "gopeed isn't installed (just agent-tools)")
    def test_gopeed_pause_and_resume_as_its_user(self):
        self.pause_and_resume("gopeed", rate=8 * MiB)

    @unittest.skipUnless(ARIA2, "aria2c isn't installed")
    def test_aria2_pause_and_resume_as_its_user(self):
        self.pause_and_resume("aria2", rate=256 * 1024)

    def test_a_planted_link_is_not_followed(self):
        # Whatever the downloader leaves in work/, the agent moves only plain
        # files it can see there, never through links, never out of the volume.
        origin = Origin(4 * MiB)
        self.addCleanup(origin.close)
        job = self.job(origin, tool="aria2")
        work = os.path.join(self.jobdir(), "work")
        os.makedirs(work)
        os.symlink("/etc", os.path.join(work, "etc"))
        os.symlink("/etc/shadow", os.path.join(work, "shadow"))
        if ARIA2:
            summary = self.finish(self.start(job))
            self.assertEqual(summary["state"], "done")
            self.assertEqual(sorted(os.listdir(os.path.join(self.root, "Downloads"))), ["file.bin"])


# ---------------------------------------------------------------- WARP

# Stand-ins for wgcf and the WARP proxy (warp/main.go), with the same
# command lines. The proxy answers SOCKS5 and HTTP CONNECT on its unix
# socket and looks "origin.test" up as 127.0.0.1 on the agent's side, which
# a downloader in its own network namespace can't reach any other way: a
# download that finishes went through the proxy.
FAKE_WGCF = """#!/bin/sh
case "$1" in
register) [ -f "$(dirname "$0")/fail" ] && { cat "$(dirname "$0")/fail"; exit 1; }; echo 'access_token = "t"' > wgcf-account.toml ;;
generate) printf '[Interface]\\nPrivateKey = x\\n' > wgcf-profile.conf ;;
esac
"""

FAKE_PROXY = r'''#!/usr/bin/env python3
import json, os, socket, struct, sys, threading
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
if "-endpoint" in args:
    print(json.dumps({"ready": False, "error": "no handshake with " + args["-endpoint"]}), flush=True)
    sys.exit(1)
path, gid = args["-listen"], int(args.get("-group", "-1"))
if os.path.exists(path):
    os.remove(path)
ln = socket.socket(socket.AF_UNIX)
ln.bind(path)
if gid >= 0:
    os.chown(path, -1, gid)
os.chmod(path, 0o660 if gid >= 0 else 0o600)
ln.listen(64)
log = os.path.join(os.path.dirname(os.path.abspath(__file__)), "targets")
def note(target):
    with open(log, "a") as f:
        f.write(target + "\n")
def resolve(host):
    return "127.0.0.1" if host == "origin.test" else host
def pipe(a, b):
    def one(x, y):
        try:
            while True:
                d = x.recv(65536)
                if not d:
                    break
                y.sendall(d)
        except OSError:
            pass
        try:
            y.shutdown(socket.SHUT_WR)
        except OSError:
            pass
    t = threading.Thread(target=one, args=(b, a), daemon=True)
    t.start()
    one(a, b)
    t.join()
    a.close(); b.close()
def recvn(c, n):
    b = b""
    while len(b) < n:
        d = c.recv(n - len(b))
        if not d:
            raise EOFError
        b += d
    return b
def handle(c):
    try:
        first = recvn(c, 1)
        if first == b"\x05":
            n = recvn(c, 1)[0]
            recvn(c, n)
            c.sendall(b"\x05\x00")
            ver, cmd, _, atyp = recvn(c, 4)
            if atyp == 1:
                host = socket.inet_ntoa(recvn(c, 4))
            elif atyp == 3:
                host = recvn(c, recvn(c, 1)[0]).decode()
            else:
                host = socket.inet_ntop(socket.AF_INET6, recvn(c, 16))
            port = struct.unpack("!H", recvn(c, 2))[0]
            note(f"socks {host}:{port}")
            up = socket.create_connection((resolve(host), port))
            c.sendall(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
            pipe(c, up)
        else:
            head = first
            while b"\r\n\r\n" not in head:
                head += recvn(c, 1)
            line = head.split(b"\r\n")[0].decode()
            method, target = line.split()[:2]
            host, port = target.rsplit(":", 1)
            note(f"connect {target}")
            up = socket.create_connection((resolve(host), int(port)))
            c.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            pipe(c, up)
    except Exception:
        c.close()
print(json.dumps({"ready": True, "warp": "on", "colo": "TST", "ip": "192.0.2.9"}), flush=True)
while True:
    c, _ = ln.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
'''


class WarpTest(AgentTest):
    def setUp(self):
        super().setUp()
        bin_dir = tempfile.mkdtemp(prefix="mfs-warpbin-")
        os.chmod(bin_dir, 0o755)
        self.addCleanup(shutil.rmtree, bin_dir, True)
        for name, text in (("wgcf", FAKE_WGCF), ("modalfs-warp", FAKE_PROXY)):
            with open(os.path.join(bin_dir, name), "w") as f:
                f.write(text)
            os.chmod(os.path.join(bin_dir, name), 0o755)
        self.bin_dir = bin_dir
        self.proxy_log = os.path.join(bin_dir, "targets")  # what the proxy connected to
        os.environ.update(WGCF_BIN=os.path.join(bin_dir, "wgcf"), MODALFS_WARP_BIN=os.path.join(bin_dir, "modalfs-warp"))
        for k in ("WGCF_BIN", "MODALFS_WARP_BIN"):
            self.addCleanup(os.environ.pop, k, None)

    def failed(self, job):
        summary = self.finish(self.start(job))
        self.assertEqual(summary["state"], "failed")
        self.assertTrue(summary["error"].startswith(app.WARP_ERROR), summary["error"])
        return summary

    def test_no_warp_means_no_download(self):
        # WARP can't be reached: the job fails saying so, and nothing is
        # downloaded directly instead.
        origin = Origin(4 * MiB)
        self.addCleanup(origin.close)
        summary = self.failed(self.job(origin, tool="aria2", warp=True, warp_endpoint="192.0.2.1:2408"))
        self.assertIn("couldn't connect", summary["error"])
        self.assertEqual(origin.served, 0)
        self.assertFalse(os.path.exists(os.path.join(self.root, "Downloads")))

    def test_identity_is_registered_once_and_kept_private(self):
        origin = Origin(1 * MiB)
        self.addCleanup(origin.close)
        self.finish(self.start(self.job(origin, tool="aria2", warp=True, warp_endpoint="192.0.2.1:2408")))
        warp = os.path.join(self.root, ".modalfs", "warp")
        self.assertEqual(sorted(os.listdir(warp)), ["wgcf-account.toml", "wgcf-profile.conf"])
        self.assertEqual(os.stat(warp).st_mode & 0o777, 0o700)
        for name in os.listdir(warp):
            self.assertEqual(os.stat(os.path.join(warp, name)).st_mode & 0o777, 0o600)

    def test_registration_refused(self):
        with open(os.path.join(self.bin_dir, "fail"), "w") as f:
            f.write("429 Too Many Requests")
        origin = Origin(1 * MiB)
        self.addCleanup(origin.close)
        summary = self.failed(self.job(origin, tool="aria2", warp=True))
        self.assertIn("try again later", summary["error"])
        self.assertEqual(origin.served, 0)

    def test_bittorrent_is_refused(self):
        origin = Origin(1 * MiB)
        self.addCleanup(origin.close)
        job = self.job(origin, tool="aria2", warp=True, url="magnet:?xt=urn:btih:" + "0" * 40)
        summary = self.failed(job)
        self.assertIn("BitTorrent", summary["error"])

    @unittest.skipIf(AS_ROOT, "on Linux as root the downloader does get a namespace")
    def test_without_a_namespace_nothing_runs(self):
        # Where the downloader can't be given a network of its own (macOS,
        # no unshare), a WARP job fails rather than run with a proxy setting
        # alone.
        origin = Origin(1 * MiB)
        self.addCleanup(origin.close)
        summary = self.failed(self.job(origin, tool="aria2", warp=True))
        self.assertIn("network of its own", summary["error"])
        self.assertEqual(origin.served, 0)


@unittest.skipUnless(AS_ROOT and shutil.which("unshare") and shutil.which("socat") and shutil.which("ip"),
                     "needs root on Linux, MODALFS_DOWNLOADER_USER, unshare, socat and ip")
class WarpNamespaceTest(WarpTest):
    """The downloader in its own network namespace, the proxy as its way out."""

    def job(self, origin, **kw):
        kw.setdefault("warp", True)
        job = super().job(origin, **kw)
        job["url"] = job["url"].replace("127.0.0.1", "origin.test")
        return job

    def targets(self):
        with open(self.proxy_log) as f:
            return f.read().split()

    def check_through_proxy(self, origin, how):
        summary = self.finish(self.start(self.job(origin, tool=self.tool)))
        self.assertEqual(summary["state"], "done", summary)
        self.check_file(origin)
        self.assertIn(how, self.targets())
        ev = [e for e in self.events() if e["ev"] == "warp"]
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["colo"], "TST")
        for way, result in ev[0]["leak_check"].items():
            self.assertTrue(result.startswith("blocked"), f"{way}: {result}")
        self.assertEqual(self.status().get("warp", {}).get("colo"), "TST")

    @unittest.skipUnless(ARIA2, "aria2c isn't installed")
    def test_aria2_through_the_proxy_only(self):
        self.tool = "aria2"
        origin = Origin(8 * MiB)
        self.addCleanup(origin.close)
        self.check_through_proxy(origin, "connect")

    @unittest.skipUnless(GOPEED, "gopeed isn't installed (just agent-tools)")
    def test_gopeed_through_the_proxy_only(self):
        self.tool = "gopeed"
        origin = Origin(8 * MiB)
        self.addCleanup(origin.close)
        self.check_through_proxy(origin, "socks")

    @unittest.skipUnless(GOPEED, "gopeed isn't installed (just agent-tools)")
    def test_gopeed_pause_and_resume_in_its_namespace(self):
        # the agent reaches gopeed's API inside the namespace (NetNS.wrap)
        self.pause_and_resume("gopeed", rate=8 * MiB)


if __name__ == "__main__":
    unittest.main()
