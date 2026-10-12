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
        events, t0 = 0, time.time()
        with urllib.request.urlopen(req, timeout=10) as r:
            for line in r:
                if line.startswith(b"data: "):
                    json.loads(line[6:])
                    events += 1
                if time.time() - t0 > 2:
                    break
        self.assertGreaterEqual(events, 6, "the live stream should send about 4 updates a second")
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


if __name__ == "__main__":
    unittest.main()
