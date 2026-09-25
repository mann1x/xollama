#!/usr/bin/env python3
"""Phase 0: how reliably does the planner route? N trials per message, each with
a fresh random seed, against the same conversation council_tree.py builds."""
import argparse, collections, json, random
import council_tree as ct

TRIVIAL = ["Hello!", "Thanks, that helps.", "hi there", "What is 2+2?", "Good morning!"]
HARD = ["Review the design in the document: what are its three weakest points and how would you fix each?",
        "Compare the device-pinning rule with how a scheduler should behave when the pinned GPU is busy; propose a policy.",
        "Write a test plan covering every failure mode the document mentions."]

ap = argparse.ArgumentParser(); ap.add_argument("--url", default="http://127.0.0.1:38311")
ap.add_argument("--doc", required=True); ap.add_argument("--n", type=int, default=20)
ap.add_argument("--temperature", type=float, default=0.7); ap.add_argument("--out", required=True)
ap.add_argument("--label", default="run")
ap.add_argument("--mode", choices=["full", "route-only"], default="full"); a = ap.parse_args()
doc = open(a.doc).read(); rng = random.Random(); nonce = "%x" % random.getrandbits(40)
conv = [{"role": "system", "content": ct.CHARTER},
        {"role": "user", "content": f"[ref {nonce}] Here is a design document I am working on:\n\n{doc}"},
        {"role": "assistant", "content": "I have read the document. What would you like to know?"}]
ROUTE_ONLY = {"type": "object", "properties": {"route": {"type": "string", "enum": ["direct", "council"]}}, "required": ["route"]}
ROUTE_MSG = {"role": "user", "content": "ROLE: PLANNER. Is the user's latest message trivial (a greeting, thanks, small talk, "
             "a one-line fact) or does it need the council? Reply with JSON only: {\"route\":\"direct\"} or {\"route\":\"council\"}."}
res = {"label": a.label, "mode": a.mode, "n": a.n, "temperature": a.temperature, "cells": {}}
for kind, msgs in (("trivial", TRIVIAL), ("hard", HARD)):
    for m in msgs:
        c = collections.Counter()
        for _ in range(a.n):
            o, _ = ct.req(a.url, "/v1/chat/completions", {"messages": conv + [{"role": "user", "content": m}, ROUTE_MSG if a.mode == "route-only" else ct.planner_msg(2)],
                "session_id": f"p0-route-{nonce}", "max_tokens": 16 if a.mode == "route-only" else 256,
                "temperature": a.temperature, "seed": rng.getrandbits(31),
                "response_format": {"type": "json_schema", "json_schema": {"name": "route",
                    "schema": ROUTE_ONLY if a.mode == "route-only" else ct.PLANNER_SCHEMA}}})
            try: c[json.loads(o["choices"][0]["message"]["content"]).get("route", "missing")] += 1
            except Exception: c["malformed"] += 1
        res["cells"][m] = {"kind": kind, **c}
        print(f"{kind:<8} {dict(c)}  {m[:60]}")
ct.req(a.url, f"/sessions/p0-route-{nonce}/close", {})
open(f"{a.out}/routing-{a.label}-{a.mode}-{nonce}.json", "w").write(json.dumps(res, indent=2))
