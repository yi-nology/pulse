#!/usr/bin/env python3
"""MCP stdio 驱动 v1.1 场景: 以 codex agent 身份建需求 → 建 bug 关联需求 → 状态流转.

用法: mcp_driver_v11.py [PULSE_HOME]  (缺省 /tmp/pulse-e2e/p1)
归因断言由 setup_v11.sh 在驱动结束后查 activity 表完成.
"""
import json, os, subprocess, sys

BIN = "/Users/demo/my_project/lacus/pulse/pulse"
home = sys.argv[1] if len(sys.argv) > 1 else "/tmp/pulse-e2e/p1"
env = dict(os.environ,
           PULSE_ACTOR="codex",
           PULSE_HOME=home,
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
                        "clientInfo": {"name": "e2e-driver-v11", "version": "1.0"}})
print("[1] initialize:", r["result"]["serverInfo"])
send("notifications/initialized", notify=True)

r = send("tools/list", {})
tools = [t["name"] for t in r["result"]["tools"]]
v11 = [t for t in tools if any(k in t for k in
       ("requirement", "bug", "submission", "release", "review", "meeting"))]
print("[2] tools:", len(tools), "v1.1:", len(v11), v11)

def call(name, args):
    r = send("tools/call", {"name": name, "arguments": args})
    if r["result"].get("isError"):
        return {"__error__": r["result"]["content"][0]["text"]}
    return json.loads(r["result"]["content"][0]["text"])

r = call("create_requirement", {"project": "demo", "title": "代理提的导出需求",
                                "desc": "agent 经 MCP 创建", "delegated_by": "zhangsan"})
print("[3] create_requirement:", json.dumps(r, ensure_ascii=False)[:160])
rid = r["ID"]

r = call("list_requirements", {"project": "demo"})
print("[4] list_requirements:", json.dumps(r, ensure_ascii=False)[:200])

r = call("create_bug", {"project": "demo", "title": "代理报的崩溃",
                        "severity": 1, "requirement_id": rid,
                        "delegated_by": "zhangsan"})
print("[5] create_bug:", json.dumps(r, ensure_ascii=False)[:160])
bid = r["ID"]

print("[6] update_bug(fixed):",
      json.dumps(call("update_bug", {"id": bid, "status": "fixed"}), ensure_ascii=False)[:120])

r = call("list_bugs", {"project": "demo", "status": "fixed"})
print("[7] list_bugs(fixed):", json.dumps(r, ensure_ascii=False)[:200])

r = call("create_review", {"project": "demo", "kind": "requirement",
                           "requirement_id": rid, "delegated_by": "zhangsan"})
print("[8] create_review:", json.dumps(r, ensure_ascii=False)[:140])

r = call("list_meetings", {"project": "demo"})
print("[9] list_meetings:", json.dumps(r, ensure_ascii=False)[:140])

proc.stdin.close(); proc.terminate()
print("DRIVER V11 DONE rid=%d bid=%d" % (rid, bid))
