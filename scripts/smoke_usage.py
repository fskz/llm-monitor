#!/usr/bin/env python3
"""One-shot usage-metrics smoke: start mock + tool, probe, print, clean up."""
import json, os, signal, subprocess, sys, time, urllib.request

os.chdir(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
env = dict(os.environ, HOME="/tmp/lm-smoke", XDG_CONFIG_HOME="/tmp/lm-smoke/.config")
mock = subprocess.Popen([sys.executable, "scripts/mock-llm.py"],
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(0.8)
tool = subprocess.Popen(["/tmp/llm-mon2", "--port", "17899"], env=env,
                        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
try:
    time.sleep(1.0)
    # fresh data dir: create the provider first
    create = urllib.request.Request(
        "http://127.0.0.1:17899/api/providers", method="POST",
        data=json.dumps({
            "name": "usage-smoke", "base_url": "http://127.0.0.1:18999/v1",
            "model": "mock-1", "prompt": "ping", "max_tokens": 64,
            "interval_sec": 0, "timeout_sec": 30, "ttft_timeout_ms": 10000,
            "ttft_slow_ms": 2000, "enabled": True, "include_usage": True,
            "api_key": "sk-smoke-1234567890",
        }).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(create, timeout=10) as r:
        pid = json.load(r)["id"]
    req = urllib.request.Request(f"http://127.0.0.1:17899/api/providers/{pid}/probe", method="POST")
    with urllib.request.urlopen(req, timeout=30) as r:
        res = json.load(r)
    keep = {k: res.get(k) for k in ("status", "ttft_ms", "total_ms", "prompt_tokens",
                                     "completion_tokens", "decode_tps", "prefill_tps", "slow")}
    print(json.dumps(keep, ensure_ascii=False, indent=2))
    # sanity: decode = 23 / ~0.4s ≈ 57.5; prefill = 48 / ~0.2s ≈ 240
    d, p = res.get("decode_tps"), res.get("prefill_tps")
    assert d and 45 <= d <= 75, f"decode_tps={d}, want ~57.5"
    assert p and 180 <= p <= 300, f"prefill_tps={p}, want ~240"
    print("SMOKE-OK")
finally:
    for proc in (tool, mock):
        proc.send_signal(signal.SIGTERM)
    try:
        tool.wait(timeout=5); mock.wait(timeout=5)
    except subprocess.TimeoutExpired:
        for proc in (tool, mock):
            proc.kill()
