#!/usr/bin/env python3
"""Phase 0 probe: the agentic council as a PolyKV pool tree, straight against
an opencoti engine (no xollama code). Measures what plans/agentic-council-chat.md
Phase 0 asks for:

  1. /props.features
  2. whether a request with a session_id and no num_ctx books a per-request
     window (read from /kv while it streams)
  3. the direct path: "Hello!" through the planner vs a plain chat turn
  4. the council tree: planner -> N researchers -> N critics -> synthesizer,
     each role's prompt/prefilled/n_pool_shared, owner cells, wall time
  5. that closing the owner releases the window and every pool

Rules from /shared/dev/docs/cerebriline-polykv-integration.md section 0 are
followed: prefixes via /apply-template + a sentinel, a byte-prefix check before
attach, workers send pool_id + their own session_id and no num_ctx, 429 waits
Retry-After, ids hold no '/', pool id 0 is valid, close at the end.

Usage: council_tree.py --url http://127.0.0.1:38311 --doc <file> --out <dir>
"""
import argparse, json, random, sys, threading, time, urllib.request, urllib.error

SENTINEL = "⁣COUNCIL-SENTINEL⁣"

def req(url, path, body=None, method=None, timeout=600):
    data = None if body is None else json.dumps(body).encode()
    r = urllib.request.Request(url + path, data=data, method=method or ("POST" if body is not None else "GET"),
                               headers={"Content-Type": "application/json"})
    while True:
        try:
            with urllib.request.urlopen(r, timeout=timeout) as resp:
                return json.loads(resp.read() or b"null"), dict(resp.headers)
        except urllib.error.HTTPError as e:
            if e.code == 429:                      # a queue, not a failure
                time.sleep(float(e.headers.get("Retry-After", "2")))
                continue
            raise RuntimeError(f"{path}: HTTP {e.code}: {e.read()[:400]!r}")

def stream_chat(url, body):
    """Streams /v1/chat/completions; returns (text, ttft_s, total_s, last_chunk)."""
    body = dict(body, stream=True)
    r = urllib.request.Request(url + "/v1/chat/completions", data=json.dumps(body).encode(),
                               headers={"Content-Type": "application/json"})
    t0 = time.perf_counter(); ttft = None; text = []; last = None
    with urllib.request.urlopen(r, timeout=600) as resp:
        for line in resp:
            line = line.strip()
            if not line.startswith(b"data:"): continue
            payload = line[5:].strip()
            if payload == b"[DONE]": break
            ev = json.loads(payload); last = ev
            for ch in ev.get("choices", []):
                piece = (ch.get("delta") or {}).get("content")
                if piece:
                    if ttft is None: ttft = time.perf_counter() - t0
                    text.append(piece)
    return "".join(text), ttft, time.perf_counter() - t0, last

def render(url, messages):
    out, _ = req(url, "/apply-template", {"messages": messages})
    return out["prompt"]

def cut(url, messages):
    """The layer for `messages`: their rendering up to and including the opener
    of the turn that follows (rule 4), found by rendering a sentinel turn."""
    full = render(url, messages + [{"role": "user", "content": SENTINEL}])
    i = full.index(SENTINEL)
    return full[:i]

def check_prefix(url, layer, messages):
    full = render(url, messages)
    if not full.startswith(layer):
        raise RuntimeError("layer is not a byte-prefix of the rendering (rule 5)")

CHARTER = """You are one member of a council of assistants that answers a user together.
The council has four roles. The PLANNER reads the conversation and decides whether
the latest user message needs the council at all; trivial messages (greetings,
thanks, small talk, a one-line fact) are answered directly. Otherwise the planner
writes a short plan and one brief per researcher. RESEARCHERS each investigate
their brief using only the conversation and their own knowledge, and report
findings with the evidence for each. CRITICS review the plan and all findings:
they point out errors, gaps, unsupported claims and disagreements between
researchers, and say which findings they would keep. The SYNTHESIZER writes the
single answer the user receives, using the findings and honouring the critiques.
Every member writes plainly, cites the part of the conversation it relies on, and
never invents facts about documents it was given. Your role for this turn is
stated in the last message."""

PLANNER_SCHEMA = {"type": "object", "properties": {
    "route": {"type": "string", "enum": ["direct", "council"]},
    "answer": {"type": "string"},
    "plan": {"type": "string"},
    "briefs": {"type": "array", "items": {"type": "string"}}},
    "required": ["route"]}

def planner_msg(n):
    return {"role": "user", "content":
        f"ROLE: PLANNER. Decide the route for the user's latest message. Reply with JSON only. "
        f"If it is trivial, {{\"route\":\"direct\",\"answer\":\"<your reply to the user>\"}}. "
        f"Otherwise {{\"route\":\"council\",\"plan\":\"<the plan>\",\"briefs\":[<exactly {n} researcher briefs>]}}."}

def jitter(t, frac, rng):
    return round(t * (1 + rng.uniform(-frac, frac)), 4)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:38311")
    ap.add_argument("--doc", required=True)
    ap.add_argument("--question", default=None)
    ap.add_argument("--researchers", type=int, default=2)
    ap.add_argument("--critics", type=int, default=2)
    ap.add_argument("--temperature", type=float, default=0.7)
    ap.add_argument("--jitter", type=float, default=0.02)
    ap.add_argument("--num-ctx", type=int, default=32768)
    ap.add_argument("--num-ctx-min", type=int, default=8192)
    ap.add_argument("--out", required=True)
    ap.add_argument("--label", default="run")
    a = ap.parse_args()
    U = a.url; rng = random.Random(); nonce = "%x" % random.getrandbits(40)
    rep = {"label": a.label, "nonce": nonce, "url": U, "roles": [], "tokens_limit_note": "max_tokens >= 256 per role"}

    props, _ = req(U, "/props"); rep["features"] = sorted(props.get("features", []))
    rep["build_info"] = props.get("build_info")

    doc = open(a.doc).read()
    # Unique knowledge per run: a stale slot cache would otherwise beat the pool.
    conv = [{"role": "system", "content": CHARTER},
            {"role": "user", "content": f"[ref {nonce}] Here is a design document I am working on:\n\n{doc}"},
            {"role": "assistant", "content": "I have read the document. What would you like to know?"}]
    question = a.question or ("Review the design in the document: what are its three weakest points, "
                              "what failure would each cause in production, and how would you fix each?")

    # --- 2. per-request window: session_id, no num_ctx, read /kv mid-stream ---
    seen = {}
    def watch(sid):
        for _ in range(40):
            kv, _ = req(U, "/kv")
            for al in kv.get("allocations", []):
                if al.get("session_id") == sid: seen.update(al); return
            time.sleep(0.05)
    sid = f"p0-perreq-{nonce}"
    th = threading.Thread(target=watch, args=(sid,)); th.start()
    stream_chat(U, {"messages": [{"role": "user", "content": "Count from 1 to 60, comma separated."}],
                    "session_id": sid, "max_tokens": 256, "temperature": 0})
    th.join()
    rep["per_request_window"] = {k: seen.get(k) for k in ("window", "requested", "cells", "per_request", "used")}
    req(U, f"/sessions/{sid}/close", {})

    # --- 3. direct path ---------------------------------------------------------
    # Both arms twice, alternating, and the second (warm) pass is the one
    # reported: the first pays the cold prefill of the document, whichever
    # arm runs first.
    hello = conv + [{"role": "user", "content": "Hello!"}]
    for rnd in range(2):
        # Same footing as the planner: a session id, so slot affinity applies.
        psid = f"p0-plain-{nonce}-{rnd}"
        _, ttft_plain, tot_plain, plast = stream_chat(U, {"messages": hello, "max_tokens": 256,
            "session_id": psid, "num_ctx": a.num_ctx, "num_ctx_min": a.num_ctx_min,
            "temperature": a.temperature, "seed": rng.getrandbits(31)})
        req(U, f"/sessions/{psid}/close", {})
        sid = f"p0-direct-{nonce}-{rnd}"
        txt, ttft_pl, tot_pl, last = stream_chat(U, {"messages": hello + [planner_msg(a.researchers)],
            "session_id": sid, "num_ctx": a.num_ctx, "num_ctx_min": a.num_ctx_min, "max_tokens": 256,
            "temperature": a.temperature, "seed": rng.getrandbits(31),
            "response_format": {"type": "json_schema", "json_schema": {"name": "route", "schema": PLANNER_SCHEMA}}})
        req(U, f"/sessions/{sid}/close", {})
    try: dec = json.loads(txt); route = dec.get("route")
    except Exception: dec, route = None, "malformed"
    tm = lambda ev: ((ev or {}).get("timings") or {})
    rep["direct"] = {"plain_ttft_s": ttft_plain, "plain_total_s": tot_plain,
                     "plain_prefill": tm(plast).get("prompt_n"), "plain_gen": tm(plast).get("predicted_n"),
                     "planner_prefill": tm(last).get("prompt_n"), "planner_gen": tm(last).get("predicted_n"),
                     "planner_ttft_s": ttft_pl, "planner_total_s": tot_pl, "route": route,
                     "answer": (dec or {}).get("answer", txt)[:300]}

    # --- 4. the council tree ------------------------------------------------------
    S = f"p0-owner-{nonce}"
    turn = conv + [{"role": "user", "content": question}]
    T0 = time.perf_counter()
    seed = rng.getrandbits(31)
    out, hdr = req(U, "/v1/chat/completions", {"messages": turn + [planner_msg(a.researchers)],
        "session_id": S, "num_ctx": a.num_ctx, "num_ctx_min": a.num_ctx_min, "max_tokens": 512,
        "temperature": a.temperature, "seed": seed,
        "response_format": {"type": "json_schema", "json_schema": {"name": "route", "schema": PLANNER_SCHEMA}}})
    rep["owner_window"] = hdr.get("X-Context-Window")
    ptxt = out["choices"][0]["message"]["content"]
    rep["roles"].append(role_row("planner", 0, seed, a.temperature, out))
    try: dec = json.loads(ptxt)
    except Exception: dec = {"route": "malformed"}
    rep["council_route"] = dec.get("route")
    if dec.get("route") != "council":                   # malformed or direct -> council anyway (never lose the question)
        dec = {"plan": "Answer the question thoroughly.", "briefs": ["Analyse the document for weaknesses."] * a.researchers}
    briefs = (dec.get("briefs") or [])[:a.researchers]
    while len(briefs) < a.researchers: briefs.append("Investigate the question from a different angle.")
    plan_msg = {"role": "assistant", "content": json.dumps({"route": "council", "plan": dec.get("plan"), "briefs": briefs})}

    base = turn + [planner_msg(a.researchers), plan_msg]
    pools = {}
    def mk(name, msgs, parent=None):
        layer = cut(U, msgs)
        body = {"prompt": layer, "session_id": S, "pin": True}
        path = "/polykv/pools" if parent is None else f"/polykv/pools/{pools[parent]['id']}/fork"
        p, _ = req(U, path, body)
        pools[name] = {"id": p["pool_id"], "resp": p, "msgs": msgs}
    t = time.perf_counter()
    mk("P1", turn)                                     # system + conversation (P0 folded: nothing varies before it)
    mk("P2r", base, "P1")
    rep["pool_build_s_P1_P2r"] = time.perf_counter() - t

    def run_role(role, i, msgs, pool, temp, max_tokens):
        check_prefix(U, cut(U, pools[pool]["msgs"]), msgs)
        s = rng.getrandbits(31)
        o, _ = req(U, "/v1/chat/completions", {"messages": msgs, "pool_id": int(pools[pool]["id"]),
            "session_id": f"{S}~{role}{i}", "max_tokens": max_tokens, "temperature": temp, "seed": s})
        req(U, f"/sessions/{S}~{role}{i}/close", {})
        return role_row(role, i, s, temp, o, pool), o["choices"][0]["message"]["content"]

    def fan(role, n, build, pool, max_tokens):
        res = [None] * n
        def go(i):
            res[i] = run_role(role, i, build(i), pool, jitter(a.temperature, a.jitter, rng), max_tokens)
        ths = [threading.Thread(target=go, args=(i,)) for i in range(n)]
        [x.start() for x in ths]; [x.join() for x in ths]
        return res

    t = time.perf_counter()
    R = fan("researcher", a.researchers, lambda i: base + [{"role": "user", "content":
            f"ROLE: RESEARCHER {i+1}. Your brief: {briefs[i]}\nReport your findings with the evidence for each."}],
            "P2r", 384)
    rep["phase_s_research"] = time.perf_counter() - t
    rep["roles"] += [r[0] for r in R]

    findings = "\n\n".join(f"FINDINGS OF RESEARCHER {i+1}:\n{r[1]}" for i, r in enumerate(R))
    fbase = base + [{"role": "user", "content": findings}]
    mk("P2f", fbase, "P2r")
    t = time.perf_counter()
    C = fan("critic", a.critics, lambda i: fbase + [{"role": "user", "content":
            f"ROLE: CRITIC {i+1}. Review the plan and all findings above: errors, gaps, unsupported claims, "
            f"disagreements. Say which findings you would keep."}], "P2f", 256)
    rep["phase_s_critique"] = time.perf_counter() - t
    rep["roles"] += [c[0] for c in C]

    critiques = "\n\n".join(f"CRITIQUE {i+1}:\n{c[1]}" for i, c in enumerate(C))
    sbase = fbase + [{"role": "user", "content": critiques}]
    mk("P3s", sbase, "P2f")
    t = time.perf_counter()
    srow, answer = run_role("synth", 0, sbase + [{"role": "user", "content":
            "ROLE: SYNTHESIZER. Write the one answer the user receives, from the findings and honouring the critiques."}],
            "P3s", a.temperature, 512)
    rep["phase_s_synth"] = time.perf_counter() - t
    rep["roles"].append(srow)
    rep["council_wall_s"] = time.perf_counter() - T0
    rep["answer_head"] = answer[:600]

    kv, _ = req(U, "/kv")
    rep["owner_mid"] = next(({k: al.get(k) for k in ("window", "cells", "used", "pools", "pressure")}
                             for al in kv.get("allocations", []) if al.get("session_id") == S), None)
    rep["pools"] = {k: {"id": v["id"], "prefix_len": v["resp"].get("prefix_len"), "own_len": v["resp"].get("own_len"), "warning": v["resp"].get("warning")}
                    for k, v in pools.items()}

    # --- 5. close ---------------------------------------------------------------
    rep["close"], _ = req(U, f"/sessions/{S}/close", {})
    kv, _ = req(U, "/kv"); pl, _ = req(U, "/polykv/pools")
    rep["after_close"] = {"allocations": len(kv.get("allocations", [])),
                          "pools": len(pl if isinstance(pl, list) else pl.get("pools", []))}

    path = f"{a.out}/council-tree-{a.label}-{nonce}.json"
    open(path, "w").write(json.dumps(rep, indent=2))
    summary(rep); print("report:", path)

def role_row(role, i, seed, temp, o, pool=None):
    u = o.get("usage", {}); t = o.get("timings", {}); oc = o.get("opencoti", {}) or {}
    return {"role": role, "i": i, "seed": seed, "temperature": temp, "pool": pool,
            "prompt_tokens": u.get("prompt_tokens"), "completion_tokens": u.get("completion_tokens"),
            "cache_n": t.get("cache_n"), "prompt_n": t.get("prompt_n"),
            "prompt_ms": t.get("prompt_ms"), "predicted_per_second": t.get("predicted_per_second"),
            "n_pool_shared": oc.get("n_pool_shared"), "pool_match": oc.get("pool_match"), "pool_len": oc.get("pool_len")}

def summary(rep):
    print("build", rep["build_info"], "| features", len(rep["features"]))
    print("per-request window:", rep["per_request_window"])
    print("direct:", {k: (round(v, 3) if isinstance(v, float) else v) for k, v in rep["direct"].items() if k != "answer"})
    print("owner window:", rep.get("owner_window"), "| council route:", rep.get("council_route"))
    print(f"{'role':<12}{'seed':>11}{'temp':>8}{'pool':>6}{'prompt':>8}{'prefill':>9}{'shared':>8}{'gen':>6}{'tok/s':>8}")
    for r in rep["roles"]:
        print(f"{r['role']+str(r['i']):<12}{r['seed']:>11}{r['temperature']:>8}{str(r['pool']):>6}{str(r['prompt_tokens']):>8}"
              f"{str(r['prompt_n']):>9}{str(r['n_pool_shared']):>8}{str(r['completion_tokens']):>6}"
              f"{(r['predicted_per_second'] or 0):>8.1f}")
    print("pools:", rep["pools"]); print("owner mid:", rep["owner_mid"])
    print("phases s: research %.2f critique %.2f synth %.2f | council wall %.2f" % (
        rep["phase_s_research"], rep["phase_s_critique"], rep["phase_s_synth"], rep["council_wall_s"]))
    print("close:", rep["close"], "| after:", rep["after_close"])

if __name__ == "__main__":
    main()
