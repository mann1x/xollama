#!/usr/bin/env python3
"""Phase 0: the direct path's cost with a route-only decision. Arm A is a plain
streamed answer. Arm B is the route-only decision (<=16 tokens) followed by the
same streamed answer on the same session, so the answer reuses the slot's cache.
Each trial uses a fresh nonce'd conversation, so every arm starts cold."""
import argparse, json, random, statistics as st, time
import council_tree as ct

ROUTE_ONLY = {"type": "object", "properties": {"route": {"type": "string", "enum": ["direct", "council"]}}, "required": ["route"]}
ROUTE_MSG = {"role": "user", "content": "ROLE: PLANNER. Is the user's latest message trivial (a greeting, thanks, small talk, "
             "a one-line fact) or does it need the council? Reply with JSON only: {\"route\":\"direct\"} or {\"route\":\"council\"}."}
ap = argparse.ArgumentParser(); ap.add_argument("--url", default="http://127.0.0.1:38311")
ap.add_argument("--doc", required=True); ap.add_argument("--n", type=int, default=5); ap.add_argument("--out", required=True)
a = ap.parse_args(); doc = open(a.doc).read(); rng = random.Random()
rows = {"plain": [], "route+answer": [], "route_only": []}
for t in range(a.n):
    for arm in ("plain", "route+answer"):
        nonce = "%x" % random.getrandbits(40); sid = f"p0-dc-{nonce}"
        conv = [{"role": "system", "content": ct.CHARTER},
                {"role": "user", "content": f"[ref {nonce}] Here is a design document I am working on:\n\n{doc}"},
                {"role": "assistant", "content": "I have read the document. What would you like to know?"},
                {"role": "user", "content": "Hello!"}]
        base = {"session_id": sid, "num_ctx": 32768, "num_ctx_min": 8192, "temperature": 0.7}
        t0 = time.perf_counter()
        if arm == "route+answer":
            o, _ = ct.req(a.url, "/v1/chat/completions", dict(base, messages=conv + [ROUTE_MSG], max_tokens=16,
                seed=rng.getrandbits(31), response_format={"type": "json_schema", "json_schema": {"name": "route", "schema": ROUTE_ONLY}}))
            rows["route_only"].append(time.perf_counter() - t0)
        t1 = time.perf_counter()
        _, ttft, _, last = ct.stream_chat(a.url, dict(base, messages=conv, max_tokens=256, seed=rng.getrandbits(31)))
        rows[arm].append(time.perf_counter() - t0)                  # whole turn, decision included
        rows.setdefault(arm + "_ttft", []).append((t1 - t0) + ttft)  # first answer token, decision included
        rows.setdefault(arm + "_prefill", []).append(((last or {}).get("timings") or {}).get("prompt_n"))
        ct.req(a.url, f"/sessions/{sid}/close", {})
out = {k: {"median": st.median(v) if v and isinstance(v[0], float) else None, "values": v} for k, v in rows.items()}
for k, v in out.items(): print(f"{k:<22} median={v['median'] if v['median'] is None else round(v['median'], 3)}  {v['values'] if 'prefill' in k else ''}")
open(f"{a.out}/direct-cost-{'%x' % random.getrandbits(32)}.json", "w").write(json.dumps(out, indent=2))
