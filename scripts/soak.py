#!/usr/bin/env python3
"""Concurrent load generator and control-plane memory monitor for maio.

It talks to a running maio service with a shared API key, mixes streaming and
non-streaming inference with a few cheap metadata requests, and samples
/api/stats so the shell wrapper can assert the 200 MiB memory target and the
absence of crashes/panics.
"""
import argparse
import http.client
import json
import random
import threading
import time
import urllib.parse


class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.requests = 0
        self.statuses = {}
        self.errors = []
        self.latencies = []
        self.latency_cap = 100000
        self.max_rss = 0
        self.max_fds = 0
        self.max_goroutines = 0
        self.max_panics = 0
        self.final = {}

    def add(self, status, latency, err=None):
        with self.lock:
            self.requests += 1
            self.statuses[str(status)] = self.statuses.get(str(status), 0) + 1
            if len(self.latencies) < self.latency_cap:
                self.latencies.append(latency)
            else:
                # Bounded reservoir keeps percentiles stable without growing
                # memory during a 24h run.
                idx = random.randrange(self.requests)
                if idx < self.latency_cap:
                    self.latencies[idx] = latency
            if err and len(self.errors) < 20:
                self.errors.append(err)

    def sample(self, d):
        with self.lock:
            self.max_rss = max(self.max_rss, d.get("rss_bytes", 0))
            self.max_fds = max(self.max_fds, d.get("open_fds", 0))
            self.max_goroutines = max(self.max_goroutines, d.get("goroutines", 0))
            self.max_panics = max(self.max_panics, d.get("panics", 0))
            self.final = d


def request(conn, method, path, body, key, timeout=60):
    headers = {"Authorization": "Bearer " + key, "Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(body)
    conn.timeout = timeout
    conn.request(method, path, body=body, headers=headers)
    resp = conn.getresponse()
    data = resp.read()
    return resp.status, data


def worker(base, key, model, deadline, stats, stop):
    host = urllib.parse.urlparse(base)
    conn = None
    while not stop.is_set() and time.time() < deadline:
        try:
            if conn is None:
                conn = http.client.HTTPConnection(host.hostname, host.port, timeout=60)
            choice = random.randrange(100)
            start = time.time()
            if choice < 45:
                status, _ = request(conn, "POST", "/api/generate",
                                    {"model": model, "prompt": "hello", "stream": False}, key)
            elif choice < 70:
                status, _ = request(conn, "POST", "/api/chat",
                                    {"model": model, "messages": [{"role": "user", "content": "hello"}],
                                     "stream": True}, key)
            elif choice < 85:
                status, _ = request(conn, "POST", "/v1/chat/completions",
                                    {"model": model, "messages": [{"role": "user", "content": "hello"}],
                                     "stream": False}, key)
            elif choice < 95:
                status, _ = request(conn, "GET", "/api/tags", None, key)
            else:
                status, _ = request(conn, "GET", "/api/ps", None, key)
            stats.add(status, time.time() - start)
        except Exception as exc:  # noqa: BLE001 - report the exact transport error
            stats.add(0, 0.0, repr(exc))
            try:
                if conn:
                    conn.close()
            except Exception:
                pass
            conn = None
            time.sleep(0.05)
    if conn:
        conn.close()


def monitor(base, key, stop, stats):
    host = urllib.parse.urlparse(base)
    while not stop.wait(2.0):
        try:
            conn = http.client.HTTPConnection(host.hostname, host.port, timeout=5)
            status, data = request(conn, "GET", "/api/stats", None, key, timeout=5)
            conn.close()
            if status == 200:
                stats.sample(json.loads(data)["metrics"])
        except Exception:
            pass


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base", required=True)
    p.add_argument("--key", required=True)
    p.add_argument("--model", default="tiny")
    p.add_argument("--duration", type=int, default=60)
    p.add_argument("--concurrency", type=int, default=64)
    p.add_argument("--report", required=True)
    args = p.parse_args()
    if args.concurrency < 1:
        p.error("--concurrency must be >= 1")
    if args.duration < 1:
        p.error("--duration must be >= 1")

    stats = Stats()
    stop = threading.Event()
    deadline = time.time() + args.duration
    monitor_thread = threading.Thread(target=monitor, args=(args.base, args.key, stop, stats), daemon=True)
    monitor_thread.start()
    threads = [threading.Thread(target=worker,
                                args=(args.base, args.key, args.model, deadline, stats, stop),
                                daemon=True) for _ in range(args.concurrency)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    stop.set()
    monitor_thread.join(timeout=5)

    lat = sorted(stats.latencies)
    def pct(q):
        if not lat:
            return 0.0
        return round(lat[min(len(lat) - 1, int(len(lat) * q))] * 1000, 3)

    with stats.lock:
        report = {
            "duration_s": args.duration,
            "concurrency": args.concurrency,
            "requests": stats.requests,
            "statuses": stats.statuses,
            "errors": stats.errors,
            "error_count": sum(v for k, v in stats.statuses.items()
                               if k == "0" or k.startswith("5")),
            "latency_ms": {"p50": pct(0.50), "p95": pct(0.95), "p99": pct(0.99), "max": pct(0.999999)},
            "max_rss_bytes": stats.max_rss,
            "max_open_fds": stats.max_fds,
            "max_goroutines": stats.max_goroutines,
            "max_panics": stats.max_panics,
            "final": stats.final,
        }
    with open(args.report, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2)
    print(json.dumps(report))


if __name__ == "__main__":
    main()
