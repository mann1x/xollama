#!/usr/bin/env python3
"""The council gate of a release (docs/protocols/RELEASE.md, "The council gate").

Three turns against each council tag named on the command line, on a running
xollama: the direct path, a convened turn, and a second turn of the same
conversation. Every call is /api/chat, not streamed. Exit 0 when every line
is PASS.

    scripts/council-gate.py --host 127.0.0.1:22498 \\
        gate/council-kv3-384k gate/council-kv3-nopolykv omni-council-idle

It creates nothing and changes no model; the tags and the server are the
protocol's to set up.
"""

import argparse
import json
import sys
import time
import urllib.error
import urllib.request

QUESTION = (
    "A tank holds 2400 litres. Pump A fills it in 40 minutes, pump B in 60. "
    "A leak drains 10 litres a minute. Starting empty with both pumps and the "
    "leak, how long until it is full? Show the reasoning and check it."
)
FOLLOW_UP = "And if the leak were 20 litres a minute?"
# 2400 / (60 + 40 - 10) = 26.67 minutes; with the larger leak 2400 / 80 = 30.
ANSWER, FOLLOW_UP_ANSWER = "26", "30"
ROLES = {"planner", "researcher", "critic", "synthesizer"}


def chat(host, model, messages, timeout):
    body = json.dumps({"model": model, "messages": messages, "stream": False}).encode()
    req = urllib.request.Request(f"http://{host}/api/chat", body, {"Content-Type": "application/json"})
    start = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status, raw = resp.status, resp.read()
    except urllib.error.HTTPError as err:
        status, raw = err.code, err.read()
    try:
        data = json.loads(raw)
    except ValueError:
        data = {"error": raw[:200].decode(errors="replace")}
    return status, data, time.time() - start


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--host", default="127.0.0.1:22434")
    ap.add_argument("--timeout", type=int, default=1800, help="seconds per call")
    ap.add_argument("models", nargs="+")
    args = ap.parse_args()

    passed = True

    def line(name, good, detail):
        nonlocal passed
        passed = passed and good
        print(("PASS " if good else "FAIL ") + name + ": " + detail, flush=True)

    for model in args.models:
        status, d, took = chat(args.host, model, [{"role": "user", "content": "Hello!"}], args.timeout)
        text = d.get("message", {}).get("content", "")
        line(f"{model} direct", status == 200 and d.get("done") is True and not d.get("error") and text.strip() != "",
             f"http {status} {took:.0f}s {text[:60]!r} {d.get('error', '')}")

        status, d, took = chat(args.host, model, [{"role": "user", "content": QUESTION}], args.timeout)
        text = d.get("message", {}).get("content", "")
        usage = d.get("council_usage") or []
        if isinstance(usage, dict):
            usage = usage.get("roles", [])
        roles = {r.get("role") for r in usage if isinstance(r, dict)}
        line(f"{model} convened",
             status == 200 and d.get("done") is True and not d.get("error") and ANSWER in text and ROLES <= roles,
             f"http {status} {took:.0f}s roles={sorted(r for r in roles if r)} answer has {ANSWER!r}: {ANSWER in text} {text[-90:]!r} {d.get('error', '')}")
        if status != 200 or not d.get("message"):
            continue

        msgs = [{"role": "user", "content": QUESTION}, d["message"], {"role": "user", "content": FOLLOW_UP}]
        status, d, took = chat(args.host, model, msgs, args.timeout)
        text = d.get("message", {}).get("content", "")
        line(f"{model} second turn",
             status == 200 and d.get("done") is True and not d.get("error") and FOLLOW_UP_ANSWER in text,
             f"http {status} {took:.0f}s answer has {FOLLOW_UP_ANSWER!r}: {FOLLOW_UP_ANSWER in text} {text[-90:]!r} {d.get('error', '')}")

    print("COUNCIL-GATE-PASS" if passed else "COUNCIL-GATE-FAIL")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
