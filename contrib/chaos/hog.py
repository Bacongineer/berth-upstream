#!/usr/bin/env python3
# Copyright 2026 Trevin Tindall (T-Crypt)
# SPDX-License-Identifier: Apache-2.0
# hog.py: a VRAM hog for the contrib/chaos scenarios. Needs one NVIDIA card
# and PyTorch. See the README beside this file for every scenario.

import argparse
import http.server
import os
import signal
import threading
import time


class slots(http.server.BaseHTTPRequestHandler):
    # One llama.cpp slot that is processing forever: a drain that waits on
    # the slot boundary never clears.
    def do_GET(self):
        body = b'[{"id":0,"is_processing":true}]'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def allocate(gib):
    import torch

    return torch.zeros(gib * (1 << 30) // 4, dtype=torch.float32, device="cuda:0")


def hold(gib, mode, port):
    ref = allocate(gib)
    assert ref is not None  # the tensor must outlive the allocation
    if mode == "busy-slots":
        srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), slots)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        print(f"hog {os.getpid()} holds {gib} GiB on cuda:0; /slots busy on 127.0.0.1:{port}", flush=True)
    else:
        print(f"hog {os.getpid()} holds {gib} GiB on cuda:0 ({mode})", flush=True)
    while True:
        time.sleep(60)


def main():
    ap = argparse.ArgumentParser(
        description="hog some VRAM on cuda:0, politely or not",
        epilog="MODE: polite | ignore-term | escape-child | busy-slots",
    )
    ap.add_argument("gib", type=int, help="GiB to hold")
    ap.add_argument("mode", choices=["polite", "ignore-term", "escape-child", "busy-slots"])
    ap.add_argument("--port", type=int, default=18080, help="the /slots port (busy-slots)")
    args = ap.parse_args()

    if args.mode == "ignore-term":
        signal.signal(signal.SIGTERM, signal.SIG_IGN)

    if args.mode == "escape-child":
        child = os.fork()
        if child:
            print(f"child {child} will take {args.gib} GiB on cuda:0; parent exiting", flush=True)
            return
        os.setsid()  # new session, no longer in the parent's group or session

    hold(args.gib, args.mode, args.port)


if __name__ == "__main__":
    main()
