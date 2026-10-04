import json, sys, time, urllib.request
H = "http://127.0.0.1:22498"
def post(path, body, timeout=900):
    r = urllib.request.Request(H + path, json.dumps(body).encode(), {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(r, timeout=timeout) as f: return f.status, f.read()
    except urllib.error.HTTPError as e: return e.code, e.read()
def chat(model, msgs, **kw):
    s, b = post("/api/chat", dict(model=model, messages=msgs, stream=False, **kw))
    try: return s, json.loads(b)
    except Exception: return s, {"error": b[:200].decode(errors="replace")}
ok = True
def line(name, good, detail):
    global ok; ok = ok and good; print(("PASS " if good else "FAIL ") + name + ": " + detail, flush=True)

import os
ONLY=os.environ.get('COUNCIL_ONLY')
# 1. three turns of one conversation, twice (two conversations), /api/chat
for model in (() if ONLY else ("llama3:latest", "qwen2.5:1.5b")):
    msgs = []
    for i, q in enumerate(["Name three rivers in Europe and one fact about each.", "Which of those is the longest, and by how much roughly?", "Now write a 200-word paragraph about that river's delta."]):
        msgs.append({"role": "user", "content": q})
        s, d = chat(model, msgs, options={"num_predict": 256, "seed": 3})
        good = s == 200 and not d.get("error") and d.get("done") and len(d.get("message", {}).get("content", "")) > 20
        line(f"chat {model} turn {i+1}", good, f"http {s} done={d.get('done')} {d.get('done_reason')} prompt_eval={d.get('prompt_eval_count')} eval={d.get('eval_count')} {d.get('error','')}"[:200])
        if not good: break
        msgs.append(d["message"])

if not ONLY:
    # 2. OpenAI shape, streaming
    r = urllib.request.Request(H + "/v1/chat/completions", json.dumps({"model": "llama3:latest", "stream": True, "max_tokens": 128, "messages": [{"role": "user", "content": "Explain what a canal lock is."}]}).encode(), {"Content-Type": "application/json"})
    n, done = 0, False
    with urllib.request.urlopen(r, timeout=300) as f:
        for raw in f:
            raw = raw.decode().strip()
            if raw == "data: [DONE]": done = True
            elif raw.startswith("data: "): n += 1
    line("openai stream", done and n > 5, f"{n} chunks, [DONE]={done}")
    
    # 3. a tool call
    tools = [{"type": "function", "function": {"name": "get_weather", "description": "Current weather for a city", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}]
    s, d = chat("mistral-small3.1:latest", [{"role": "user", "content": "What is the weather in Lisbon right now? Use the tool."}], tools=tools, options={"num_predict": 200, "seed": 1})
    tc = d.get("message", {}).get("tool_calls") or []
    line("tool call", s == 200 and len(tc) > 0 and tc[0]["function"]["name"] == "get_weather", f"http {s} calls={json.dumps(tc)[:120]} {d.get('error','')}")
    msgs = [{"role": "user", "content": "What is the weather in Lisbon right now? Use the tool."}, d.get("message", {}), {"role": "tool", "content": "18 C, light rain", "tool_name": "get_weather"}]
    s, d = chat("mistral-small3.1:latest", msgs, tools=tools, options={"num_predict": 120, "seed": 1})
    line("tool result turn", s == 200 and d.get("done") and "18" in d.get("message", {}).get("content", ""), f"http {s} -> {d.get('message',{}).get('content','')[:90]!r} {d.get('error','')}")

# 4. council: the direct path, then a convened turn, then a second turn of the same conversation
for cm in sys.argv[1:]:
    t = time.time(); s, d = chat(cm, [{"role": "user", "content": "Hello!"}])
    line(f"council {cm} direct", s == 200 and d.get("done") and not d.get("error"), f"http {s} {time.time()-t:.0f}s -> {d.get('message',{}).get('content','')[:60]!r} {d.get('error','')}")
    q = "A tank holds 2400 litres. Pump A fills it in 40 minutes, pump B in 60. A leak drains 10 litres a minute. Starting empty with both pumps and the leak, how long until it is full? Show the reasoning and check it."
    t = time.time(); s, d = chat(cm, [{"role": "user", "content": q}])
    u = d.get("council_usage")
    line(f"council {cm} convened", s == 200 and d.get("done") and not d.get("error") and "26" in json.dumps(d.get("message", {})), f"http {s} {time.time()-t:.0f}s usage roles={[r.get('role') for r in (u or {}).get('roles', [])] if isinstance(u, dict) else u} -> {d.get('message',{}).get('content','')[-110:]!r} {d.get('error','')}")
    if s == 200 and d.get("message"):
        t = time.time(); s2, d2 = chat(cm, [{"role": "user", "content": q}, d["message"], {"role": "user", "content": "And if the leak were 20 litres a minute?"}])
        line(f"council {cm} second turn", s2 == 200 and d2.get("done") and not d2.get("error"), f"http {s2} {time.time()-t:.0f}s -> {d2.get('message',{}).get('content','')[-110:]!r} {d2.get('error','')}")
print("CHAT-ALL-PASS" if ok else "CHAT-HAS-FAILURES")
