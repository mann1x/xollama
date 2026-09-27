#!/usr/bin/env python3
"""Is a deny-listed architecture correct with several sequences in flight?

ollama/ollama#4165 holds qwen35, qwen3next, lfm2, nemotron_h and others to one
sequence because stock llama.cpp answered wrongly with more. xollama lifts that
on opencoti; this is the measurement behind it, run through xollama itself.

Four long tasks whose answers can be checked mechanically are run one at a
time, then all at once, greedy and seeded. Two scores:

  correct   each answer carries its own task's content (and not another's) --
            the #4165 symptom was sequences bleeding into each other
  agree     the concurrent answer equals the serial one byte for byte, or
            shares a long common prefix (batched kernels may round
            differently and diverge late; that is not contamination)

  parallel_correctness.py --host 127.0.0.1:22434 --model qwen3.5:2b --out f.json
"""
import argparse, json, os, time, urllib.request
from concurrent.futures import ThreadPoolExecutor

TASKS = [
    ("count", "Count upward from 200 to 330, one number per line, nothing else.",
     lambda s: all(str(n) in s for n in range(200, 300))),
    ("primes", "List the first 60 prime numbers separated by commas, nothing else.",
     lambda s: all(str(p) in s for p in (2, 3, 5, 7, 11, 13, 101, 103, 107, 109, 113, 199, 211))),
    ("table17", "Write the multiplication table of 17 from 17 x 1 up to 17 x 30, one line each, like '17 x 3 = 51'.",
     lambda s: all(str(17 * k) in s for k in (3, 7, 12, 19, 25))),
    ("alphabet", "Write the English alphabet in capital letters, space separated, forwards then backwards, ten times.",
     lambda s: "A B C D E F G" in s and "Z Y X W V" in s),
]
OTHERS = {"count": "17 x", "primes": "Z Y X", "table17": "Z Y X", "alphabet": "17 x"}


def chat(host, model, prompt, seed):
    body = {"model": model, "stream": False, "think": False,
            "messages": [{"role": "user", "content": prompt}],
            "options": {"temperature": 0, "seed": seed, "num_predict": 512, "num_ctx": 4096}}
    req = urllib.request.Request(f"http://{host}/api/chat", json.dumps(body).encode(),
                                 {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=900) as r:
        d = json.load(r)
    return d["message"]["content"], d.get("eval_count", 0)


def common_prefix(a, b):
    n = 0
    for x, y in zip(a, b):
        if x != y:
            break
        n += 1
    return n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default=os.environ.get("XOLLAMA_HOST", "127.0.0.1:22434"))
    ap.add_argument("--model", required=True)
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()

    chat(a.host, a.model, "Say ok.", 1)  # load
    rows = []
    for rnd in range(a.rounds):
        serial = [chat(a.host, a.model, p, 42) for _, p, _ in TASKS]
        t0 = time.time()
        with ThreadPoolExecutor(len(TASKS)) as ex:
            conc = list(ex.map(lambda t: chat(a.host, a.model, t[1], 42), TASKS))
        wall = time.time() - t0
        for (name, _, ok), (s, sn), (c, cn) in zip(TASKS, serial, conc):
            rows.append({"round": rnd, "task": name,
                         "serial_ok": ok(s), "parallel_ok": ok(c),
                         "parallel_foreign": OTHERS[name] in c,
                         "identical": s == c, "prefix": common_prefix(s, c),
                         "len_serial": len(s), "len_parallel": len(c),
                         "tokens_serial": sn, "tokens_parallel": cn})
        rows.append({"round": rnd, "parallel_wall_s": round(wall, 2)})
    tasks = [r for r in rows if "task" in r]
    summary = {k: sum(1 for r in tasks if r[k]) for k in ("serial_ok", "parallel_ok", "parallel_foreign", "identical")}
    summary["n"] = len(tasks)
    summary["min_prefix"] = min(r["prefix"] for r in tasks)
    res = {"model": a.model, "host": a.host, "summary": summary, "rows": rows}
    json.dump(res, open(a.out, "w"), indent=1)
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
