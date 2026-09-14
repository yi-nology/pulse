#!/usr/bin/env python3
"""MCP stdio 真实驱动: 以 codex agent 身份连接 pulse mcp, 走完整 JSON-RPC 会话."""
import json, os, subprocess, sys

BIN = "/Users/demo/my_project/lacus/pulse/pulse"
env = dict(os.environ,
           PULSE_ACTOR="codex",
           PULSE_HOME="/tmp/pulse-e2e/p1",
           PULSE_FEISHU_ENDPOINT="http://127.0.0.1:19090")

proc = subprocess.Popen([BIN, "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=subprocess.PIPE, env=env, text=True, bufsize=1)

_next_id = [0]
def send(method, params=None, notify=False):
    msg = {"jsonrpc": "2.0", "method": method}
    if not notify:
        _next_id[0] += 1
        msg["id"] = _next_id[0]
    if params is not None:
        msg["params"] = params
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()
    if notify:
        return None
    while True:
        line = proc.stdout.readline()
        if not line:
            raise RuntimeError("server closed: " + proc.stderr.read())
        resp = json.loads(line)
        if resp.get("id") == _next_id[0]:
            return resp

r = send("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                        "clientInfo": {"name": "e2e-driver", "version": "1.0"}})
print("[1] initialize:", r["result"]["serverInfo"])
send("notifications/initialized", notify=True)

r = send("tools/list", {})
tools = [t["name"] for t in r["result"]["tools"]]
print("[2] tools:", len(tools), tools)

def call(name, args):
    r = send("tools/call", {"name": name, "arguments": args})
    if r["result"].get("isError"):
        return {"__error__": r["result"]["content"][0]["text"]}
    return json.loads(r["result"]["content"][0]["text"])

print("[3] add_task(me):", json.dumps(call("add_task", {
    "project": "demo", "title": "SQL 注入扫描", "assignee": "me",
    "estimate_days": 1, "due": "2026-09-16"}), ensure_ascii=False)[:160])

r = call("list_tasks", {"project": "demo", "assignee": "me"})
print("[4] list_tasks(me):", json.dumps(r, ensure_ascii=False)[:200])
tid = r[0]["ID"]

print("[5] update_task(done, delegated_by=zhangsan):",
      json.dumps(call("update_task", {"id": tid, "status": "done", "delegated_by": "zhangsan"}),
                 ensure_ascii=False)[:120])

print("[6] get_workload:", json.dumps(call("get_workload", {"project": "demo"}), ensure_ascii=False)[:260])

print("[7] update_task(archived id=3 应拒):",
      call("update_task", {"id": 3, "title": "复活"})["__error__"][:60])

r = call("publish_feishu", {"project": "demo", "report": "weekly"})
print("[8] publish_feishu:", json.dumps(r, ensure_ascii=False)[:200])

proc.stdin.close(); proc.terminate()
print("DRIVER DONE")
