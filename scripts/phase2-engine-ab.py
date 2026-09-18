#!/usr/bin/env python3
"""Phase 2 A/B: measure xollama's two GGML engines against each other.

Phase 0 established that the opencoti-llamafile engine loads, routes and
accounts for memory identically to stock llama.cpp. It deliberately did not
measure four things, and this harness measures exactly those:

  throughput  single-stream prompt-eval and generation rate
  multislot   the same model under OLLAMA_NUM_PARALLEL > 1
  gemma4      a Gemma-4 model's tool-call and thinking-channel parsing
  overflow    a model larger than VRAM, where the layer split and rolling-KV
              spill actually do something

Each axis runs against a server this script starts and stops itself, so the
two engines never share a process or a loaded runner. Numbers come from the
API's own timing fields, not from wall-clock guesses, except where aggregate
concurrency makes wall-clock the only honest measure.

  scripts/phase2-engine-ab.py --engine opencoti --axis throughput
  scripts/phase2-engine-ab.py --engine llamacpp --axis all
"""

import argparse
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

REPO = pathlib.Path(__file__).resolve().parent.parent
DEFAULT_OUT = pathlib.Path("/srv/ml/xollama-phase2")

# A prompt long enough that prompt-eval rate is a measurement and not noise.
LONG_PROMPT = (
    "You are reading a change log. Summarise it in one sentence.\n\n"
    + "\n".join(
        f"- commit {i:04d}: adjust the scheduler so that a runner which has "
        f"been idle for longer than the keep-alive window is unloaded before "
        f"a new load is admitted, rather than after it." for i in range(120)
    )
)

SHORT_PROMPT = "Why is the sky blue? Answer in two sentences."


def post(host, path, payload, timeout=900):
    req = urllib.request.Request(
        f"http://{host}{path}",
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


def wait_ready(host, timeout=120):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(f"http://{host}/api/version", timeout=2).read()
            return True
        except Exception:
            time.sleep(1)
    return False


class Server:
    """One xollama serve, pinned to one engine."""

    def __init__(self, engine, host, models, artifact, logdir, parallel=None):
        self.engine, self.host, self.logdir = engine, host, logdir
        self.env = dict(os.environ)
        self.env.update(
            XOLLAMA_HOST=host,
            XOLLAMA_MODELS=models,
            XOLLAMA_ENGINE=engine,
            # Verbosity 5 is what makes the engine print its allocation plan;
            # the scheduler scrapes those lines for memTotal/memGPU.
            XOLLAMA_DEBUG="1",
        )
        if artifact:
            self.env["XOLLAMA_ENGINE_PATH"] = artifact
        if parallel:
            self.env["XOLLAMA_NUM_PARALLEL"] = str(parallel)
        self.proc = None
        self.logpath = None

    def __enter__(self):
        tag = f"{self.engine}-{int(time.time())}"
        self.logpath = self.logdir / f"serve-{tag}.log"
        self.log = open(self.logpath, "wb")
        self.proc = subprocess.Popen(
            [str(REPO / "xollama"), "serve"],
            cwd=REPO, env=self.env, stdout=self.log, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        if not wait_ready(self.host):
            self.__exit__(None, None, None)
            raise RuntimeError(f"{self.engine}: server did not become ready")
        return self

    def __exit__(self, *exc):
        if self.proc and self.proc.poll() is None:
            # The whole process group: a runner subprocess outliving the server
            # would hold VRAM and poison the next engine's measurement.
            os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
            try:
                self.proc.wait(timeout=30)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(self.proc.pid), signal.SIGKILL)
        if getattr(self, "log", None):
            self.log.close()
        # Let VRAM actually come back before the next run measures it.
        time.sleep(5)
        return False

    def engine_line(self):
        """What the server said it routed to -- the claim we are A/B-ing."""
        try:
            text = self.logpath.read_text(errors="replace")
        except OSError:
            return ""
        # The decision line, not the config dump -- which also contains the
        # word "opencoti" whenever XOLLAMA_ENGINE_PATH is set.
        for line in text.splitlines():
            if "source=opencoti.go" in line and "msg=" in line:
                return line.split("msg=", 1)[1].strip()[:300]
        return ""


def rates(resp):
    """Tokens per second from the API's own nanosecond counters."""
    out = {}
    pc, pd = resp.get("prompt_eval_count"), resp.get("prompt_eval_duration")
    ec, ed = resp.get("eval_count"), resp.get("eval_duration")
    if pc and pd:
        out["prompt_tok_s"] = round(pc / (pd / 1e9), 2)
        out["prompt_eval_count"] = pc
    if ec and ed:
        out["gen_tok_s"] = round(ec / (ed / 1e9), 2)
        out["eval_count"] = ec
    for k in ("load_duration", "total_duration"):
        if resp.get(k):
            out[k + "_ms"] = round(resp[k] / 1e6, 1)
    return out


def generate(host, model, prompt, num_predict=128, **opts):
    options = {"temperature": 0, "seed": 42, "num_predict": num_predict}
    options.update(opts)
    return post(host, "/api/generate", {
        "model": model, "prompt": prompt, "stream": False, "options": options,
    })


def axis_throughput(srv, model, iters):
    generate(srv.host, model, SHORT_PROMPT, num_predict=8)  # warm the runner
    runs = []
    for i in range(iters):
        # A unique prefix per iteration. Repeating one prompt measures the
        # prefix cache, not prompt eval -- it reported 344k tok/s that way.
        prompt = f"Run {i} of {iters}, nonce {i * 7919}.\n" + LONG_PROMPT
        runs.append(rates(generate(srv.host, model, prompt, num_predict=256,
                                   num_ctx=8192)))
    return {"model": model, "iterations": iters, "runs": runs,
            "median_gen_tok_s": median([r.get("gen_tok_s", 0) for r in runs]),
            "median_prompt_tok_s": median([r.get("prompt_tok_s", 0) for r in runs])}


def axis_multislot(srv, model, parallel):
    generate(srv.host, model, SHORT_PROMPT, num_predict=8)
    start = time.time()
    with ThreadPoolExecutor(max_workers=parallel) as pool:
        futures = [
            # ignore_eos would be cleaner, but /api/generate does not expose
            # it; a prompt that cannot be answered briefly plus a 512 floor
            # keeps every slot busy long enough for the wall clock to mean
            # something.
            pool.submit(generate, srv.host, model,
                        f"Write a long, detailed essay (variant {i}, nonce "
                        f"{i * 104729}) about the history of computing. Do not "
                        f"stop early.", 512)
            for i in range(parallel)
        ]
        responses = [f.result() for f in futures]
    wall = time.time() - start
    per = [rates(r) for r in responses]
    total_gen = sum(p.get("eval_count", 0) for p in per)
    return {"model": model, "parallel": parallel, "wall_s": round(wall, 2),
            "aggregate_gen_tok_s": round(total_gen / wall, 2),
            "per_request": per}


def axis_gemma4(srv, model):
    """Parsing, not speed: a tool call and a thinking turn must come back split."""
    tools = [{
        "type": "function",
        "function": {
            "name": "get_weather",
            "description": "Get the current weather for a city",
            "parameters": {
                "type": "object",
                "properties": {"city": {"type": "string"}},
                "required": ["city"],
            },
        },
    }]
    tool_resp = post(srv.host, "/api/chat", {
        "model": model, "stream": False, "tools": tools,
        "messages": [{"role": "user", "content": "What is the weather in Berlin?"}],
        "options": {"temperature": 0, "seed": 42},
    })
    msg = tool_resp.get("message", {})
    calls = msg.get("tool_calls") or []

    think_resp = post(srv.host, "/api/chat", {
        "model": model, "stream": False, "think": True,
        "messages": [{"role": "user", "content": "What is 17 * 23? Think first."}],
        "options": {"temperature": 0, "seed": 42, "num_predict": 512},
    })
    tmsg = think_resp.get("message", {})
    return {
        "model": model,
        "tool_call": {
            "count": len(calls),
            "name": calls[0]["function"]["name"] if calls else None,
            "arguments": calls[0]["function"].get("arguments") if calls else None,
            # A leaked call is the failure these parsers exist to prevent.
            "content_leaked_markup": any(
                t in (msg.get("content") or "")
                for t in ("<tool_call", "```json", "functioncall")
            ),
        },
        "thinking": {
            "separated": bool(tmsg.get("thinking")),
            "thinking_chars": len(tmsg.get("thinking") or ""),
            "content_chars": len(tmsg.get("content") or ""),
            "content_leaked_channel": any(
                t in (tmsg.get("content") or "")
                for t in ("<think", "</think", "analysis", "<start_of_turn>")
            ),
        },
    }


def axis_compat(srv, models):
    """Which models the engine can load at all.

    Phase 0 measured one plain text model and concluded the swap was low-risk.
    It is not: architecture support and the projector convention both differ,
    and a model that fails to load is a worse outcome than a slow one.
    """
    out = []
    for m in models:
        row = {"model": m}
        t0 = time.time()
        try:
            generate(srv.host, m, "hi", num_predict=1)
            row["loaded"] = True
        except urllib.error.HTTPError as e:
            row["loaded"] = False
            try:
                row["error"] = json.loads(e.read()).get("error", "")[:400]
            except Exception:
                row["error"] = f"HTTP {e.code}"
        except Exception as e:
            row["loaded"] = False
            row["error"] = f"{type(e).__name__}: {e}"[:400]
        row["seconds"] = round(time.time() - t0, 1)
        out.append(row)
    return {"models": out,
            "loaded": sum(1 for r in out if r["loaded"]),
            "total": len(out)}


def axis_overflow(srv, model):
    """A model bigger than VRAM: does it load, and at what rate."""
    t0 = time.time()
    try:
        resp = generate(srv.host, model, SHORT_PROMPT, num_predict=64)
    except (urllib.error.HTTPError, urllib.error.URLError, OSError) as e:
        return {"model": model, "loaded": False, "error": str(e)[:300],
                "seconds": round(time.time() - t0, 1)}
    out = {"model": model, "loaded": True, "seconds": round(time.time() - t0, 1)}
    out.update(rates(resp))
    try:
        ps = json.load(urllib.request.urlopen(f"http://{srv.host}/api/ps", timeout=30))
        for m in ps.get("models", []):
            if m.get("name", "").startswith(model.split(":")[0]):
                out["size_bytes"] = m.get("size")
                out["size_vram_bytes"] = m.get("size_vram")
                if m.get("size"):
                    out["fraction_in_vram"] = round(
                        (m.get("size_vram") or 0) / m["size"], 3)
    except Exception:
        pass
    return out


def median(xs):
    xs = sorted(x for x in xs if x)
    if not xs:
        return 0
    mid = len(xs) // 2
    return xs[mid] if len(xs) % 2 else round((xs[mid - 1] + xs[mid]) / 2, 2)


AXES = ("compat", "throughput", "multislot", "gemma4", "overflow")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--engine", required=True, choices=("llamacpp", "opencoti"))
    ap.add_argument("--axis", default="all", choices=AXES + ("all",))
    ap.add_argument("--host", default="127.0.0.1:11499")
    ap.add_argument("--models", default=os.environ.get("XOLLAMA_MODELS", ""))
    ap.add_argument("--artifact", default=os.environ.get("XOLLAMA_ENGINE_PATH", ""))
    ap.add_argument("--out", default=str(DEFAULT_OUT))
    ap.add_argument("--iters", type=int, default=3)
    ap.add_argument("--parallel", type=int, default=4)
    ap.add_argument("--model-throughput", default="llama3:latest")
    ap.add_argument("--model-multislot", default="qwen2.5:1.5b")
    ap.add_argument("--model-gemma4", default="gemma4:e4b")
    ap.add_argument("--models-compat", default=",".join([
        "llama3:latest", "qwen2.5:1.5b", "tinyllama:latest",
        "gemma4:e4b", "gemma3:27b-it-qat", "qwen3.5:2b",
        "mistral-small3.1:latest", "llama3.1:70b-instruct-q3_K_S",
    ]))
    ap.add_argument("--model-overflow", default="llama3.1:70b-instruct-q3_K_S")
    args = ap.parse_args()

    out = pathlib.Path(args.out)
    logs = out / "logs"
    logs.mkdir(parents=True, exist_ok=True)

    axes = AXES if args.axis == "all" else (args.axis,)
    results = {"engine": args.engine, "host": args.host,
               "started": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "axes": {}}

    for axis in axes:
        # Only the multislot axis wants more than one slot; giving every axis
        # -np 4 would silently change what the other three measure.
        parallel = args.parallel if axis == "multislot" else None
        print(f"[{args.engine}] {axis} ...", flush=True)
        t0 = time.time()
        try:
            with Server(args.engine, args.host, args.models, args.artifact,
                        logs, parallel) as srv:
                fn = {"compat": lambda: axis_compat(srv, args.models_compat.split(",")),
                      "throughput": lambda: axis_throughput(srv, args.model_throughput, args.iters),
                      "multislot": lambda: axis_multislot(srv, args.model_multislot, args.parallel),
                      "gemma4": lambda: axis_gemma4(srv, args.model_gemma4),
                      "overflow": lambda: axis_overflow(srv, args.model_overflow)}[axis]
                data = fn()
                data["routed_to"] = srv.engine_line()
                data["server_log"] = str(srv.logpath)
        except Exception as e:  # one axis failing must not lose the others
            data = {"error": f"{type(e).__name__}: {e}"[:500]}
        data["seconds_total"] = round(time.time() - t0, 1)
        results["axes"][axis] = data
        print(f"[{args.engine}] {axis} done in {data['seconds_total']}s", flush=True)

    dest = out / f"results-{args.engine}.json"
    if dest.exists():  # merge, so axes can be run one at a time
        prev = json.loads(dest.read_text())
        prev.get("axes", {}).update(results["axes"])
        prev["started"] = results["started"]
        results = prev
    dest.write_text(json.dumps(results, indent=2))
    print(f"wrote {dest}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
