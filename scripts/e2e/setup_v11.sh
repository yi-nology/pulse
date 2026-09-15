#!/bin/bash
# v1.1 研发交付闭环 E2E（可重放）：
#   重建仿真飞书 + 双机 HOME → p1 全链（需求→评审→会议→bug→提测→发版）
#   → bind（8 表）→ sync → 记录文档模板断言（仿真飞书 dump 5 类标题）
#   → MCP agent 建需求/bug + activity 归因断言
#   → 第二台机器 adopt 既有 base + 六实体表收敛（双向）
# 依赖：go、sqlite3、python3；不访问真实飞书（PULSE_FEISHU_ENDPOINT 指向本地仿真）。
set -e
P=/Users/demo/my_project/lacus/pulse/pulse
REPO=/Users/demo/my_project/lacus/pulse
E=/tmp/pulse-e2e
ENDPOINT=http://127.0.0.1:19090

fail() { echo "FAILED: $1" >&2; exit 1; }

# ---- 0. 重建并启动仿真飞书（内存态，重启即净空，token 序列可预期）----
pkill -f fakefeishu 2>/dev/null || true
sleep 0.3
mkdir -p "$E/fakefeishu"
cp "$REPO/scripts/e2e/fakefeishu/main.go" "$E/fakefeishu/main.go"
cd "$E/fakefeishu"
[ -f go.mod ] || go mod init fakefeishu >/dev/null 2>&1
go build -o fakefeishu . || fail "编译仿真飞书"
(nohup ./fakefeishu > "$E/fake.log" 2>&1 &)
for i in $(seq 1 50); do
  curl -s -o /dev/null "$ENDPOINT/_debug/dump" && break
  sleep 0.1
done
curl -s "$ENDPOINT/_debug/dump" | grep -q '"bases"' || fail "仿真飞书未就绪（见 $E/fake.log）"

# ---- 1. 双机净空 + 配置 ----
rm -rf "$E/p1" "$E/p2"
mkdir -p "$E/p1" "$E/p2"
printf 'default_actor: zhangsan\nfeishu:\n  app_id: cli_fake\n  app_secret: fake_secret\nsync:\n  stale_minutes: 10\n' > "$E/p1/config.yaml"
printf 'default_actor: lisi\nfeishu:\n  app_id: cli_fake\n  app_secret: fake_secret\nsync:\n  stale_minutes: 10\n' > "$E/p2/config.yaml"

export PULSE_HOME="$E/p1" PULSE_FEISHU_ENDPOINT="$ENDPOINT"
$P init demo --name 演示项目 >/dev/null
$P member add zhangsan >/dev/null
$P member add lisi >/dev/null
$P member add codex --type agent >/dev/null
$P version add v1.0 --project demo --target 2026-09-30 >/dev/null
$P task add '登录接口开发' --project demo --assignee zhangsan --estimate 3 --start 2026-09-10 --due 2026-09-18 --version v1.0 >/dev/null
$P task add '登录接口自测' --project demo --assignee zhangsan --estimate 1 --start 2026-09-19 --due 2026-09-20 --version v1.0 >/dev/null

# ---- 2. 全链：需求 → 评审 → 会议 → bug → 提测 → 发版 ----
out=$($P requirement add '支持扫码登录' --project demo --owner zhangsan --priority 2) \
  && echo "$out" | grep -q '需求已创建: 支持扫码登录 (id=1)' || fail "requirement add"
echo "$out" | grep -q '协作文档已创建' || fail "requirement add 未自动建文档"
$P review record --project demo --kind requirement --requirement 1 | grep -q '评审已记录: requirement (id=1)' || fail "review record"
$P review conclude 1 --conclusion passed | grep -q '评审结论已更新: passed (id=1)' || fail "review conclude"
$P meeting record '需求评审会' --project demo | grep -q '会议已记录: 需求评审会 (id=1)' || fail "meeting record"
$P task add '扫码登录联调' --project demo --assignee lisi --due 2026-09-25 >/dev/null   # 拆任务（CLI 暂无 --requirement 关联旗标）
$P bug add '扫码后页面白屏' --project demo --severity 2 --assignee zhangsan --requirement 1 --found-version v1.0 | grep -q 'Bug 已创建: 扫码后页面白屏 (id=1)' || fail "bug add"
$P submit create --project demo --version v1.0 --requirement 1 --test-owner lisi | grep -q '提测单已创建 (id=1)' || fail "submit create"
$P submit update 1 --status submitted | grep -q '提测单已更新: status=submitted (id=1)' || fail "submit update"
$P release new --project demo --version v1.0 --manager zhangsan | grep -q '发版记录已创建 (id=1)' || fail "release new"
$P release update 1 --status released | grep -q '发版已更新: status=released (id=1)' || fail "release update"
$P requirement update 1 --status accepted | grep -q '需求已更新: 支持扫码登录 (id=1)' || fail "requirement update"

# ---- 3. bind（任务/版本 + 六实体表共 8 张）+ 首次 sync ----
$P feishu bind --project demo | tee "$E/bind.out" | head -5
grep -q 'base(app_token):  app1' "$E/bind.out" || fail "bind app_token"
sqlite3 "$E/p1/pulse.db" "SELECT feishu_tables_json FROM projects WHERE id=1;" | grep -q '"requirements":"tbl3"' || fail "feishu_tables_json 六表 id 未写回"
$P sync --project demo | tee "$E/sync1.out"
grep -q '同步完成: 已推送 10、拉取 0' "$E/sync1.out" || fail "首次 sync 应推送 3 任务+v1.0+六实体共 10 条"

# ---- 4. 记录文档模板断言（仿真飞书 dump：5 类标题 + token 回填）----
curl -s "$ENDPOINT/_debug/dump" > "$E/dump.json"
python3 - "$E/dump.json" <<'PYDOC' || fail "记录文档模板标题断言"
import json, sys
d = json.load(open(sys.argv[1]))
titles = sorted(doc["title"] for doc in d["docs"].values())
expect = sorted([
    "需求 · 支持扫码登录",
    "评审记录 · requirement · 需求#1",
    "会议纪要 · 需求评审会",
    "提测单 · v1.0 · #1",
    "发版记录 · v1.0",
])
missing = [t for t in expect if t not in titles]
if missing:
    sys.exit("缺少模板文档: %s（实际: %s）" % (missing, titles))
tables = d["bases"]["app1"]["tables"]
names = sorted(t["name"] for t in tables.values())
expect_tbl = sorted(["任务表", "版本表", "需求表", "评审表", "会议表", "bug表", "提测表", "发版表"])
if names != expect_tbl:
    sys.exit("base 表集合不符: %s" % names)
for t in tables.values():
    want = {"任务表": 3, "版本表": 1, "需求表": 1, "评审表": 1, "会议表": 1, "bug表": 1, "提测表": 1, "发版表": 1}[t["name"]]
    if len(t["records"]) != want:
        sys.exit("%s 记录数 %d != %d" % (t["name"], len(t["records"]), want))
print("DOC TEMPLATES OK (5 类标题 + 8 表 + 各表记录数)")
PYDOC
[ "$(sqlite3 "$E/p1/pulse.db" "SELECT feishu_doc_token FROM requirements WHERE id=1;")" = "doc1" ] || fail "需求文档 token 未回填"

# ---- 5. MCP agent 建需求/bug（归因断言在下一步）----
python3 "$REPO/scripts/e2e/mcp_driver_v11.py" "$E/p1" | tee "$E/mcp_v11.out"
grep -q 'DRIVER V11 DONE' "$E/mcp_v11.out" || fail "MCP 驱动未完成"
grep -q 'v1.1: 15' "$E/mcp_v11.out" || fail "MCP 工具数应为 12+15=27"
sqlite3 "$E/p1/pulse.db" "SELECT count(*) FROM activity a JOIN members m ON m.id=a.actor_id
  WHERE a.actor_type='agent' AND m.name='codex' AND a.on_behalf_of=(SELECT id FROM members WHERE name='zhangsan')
    AND a.action='create' AND a.entity_type IN ('requirement','bug','review');" | grep -q '^3$' \
  || fail "agent 归因断言：应恰好 3 条 delegated create（requirement/bug/review）"

# ---- 6. 第二台机器：adopt 既有 base + 六实体表收敛 ----
unset PULSE_HOME
PULSE_HOME="$E/p2" $P init demo --name 演示项目 >/dev/null
PULSE_HOME="$E/p2" $P member add codex --type agent >/dev/null
# 采用模式直接带六实体表 id（创建方 bind 共享提示整行复制）
PULSE_HOME="$E/p2" $P feishu bind --project demo --app-token app1 --task-table tbl1 --version-table tbl2 --doc doc6 \
  --requirements-table tbl3 --reviews-table tbl4 --meetings-table tbl5 \
  --bugs-table tbl6 --submissions-table tbl7 --releases-table tbl8 | grep -q '已绑定既有飞书 base' || fail "p2 adopt bind"
grep -q -- '--requirements-table tbl3' "$E/bind.out" || fail "p1 bind 共享提示未含六表 id"
PULSE_HOME="$E/p2" $P sync --project demo | tee "$E/sync_p2.out"
grep -q '同步完成' "$E/sync_p2.out" || fail "p2 六实体 sync"
PULSE_HOME="$E/p2" $P requirement list --project demo | grep -q '支持扫码登录' || fail "p2 未拉到需求 1"
PULSE_HOME="$E/p2" $P requirement list --project demo | grep -q '代理提的导出需求' || fail "p2 未拉到 agent 需求 2"
PULSE_HOME="$E/p2" $P bug list --project demo | grep -q '扫码后页面白屏' || fail "p2 未拉到 bug 1"
PULSE_HOME="$E/p2" $P submit list --project demo | grep -q 'submitted' || fail "p2 未拉到提测单"
PULSE_HOME="$E/p2" $P release list --project demo | grep -q 'released' || fail "p2 未拉到发版记录"
PULSE_HOME="$E/p2" $P meeting list --project demo | grep -q '需求评审会' || fail "p2 未拉到会议记录"

# ---- 7. p2 → p1 方向：p2 改需求状态推回，p1 拉回收敛 ----
# 注意：pull 建行的本地 ID 与创建机不保证一致（远端记录遍历序不定），跨机定位一律按标题。
sleep 1.1 # 覆盖警告以秒级时间戳判定，隔过秒边界使其可断言
RID=$(sqlite3 "$E/p2/pulse.db" "SELECT id FROM requirements WHERE title='代理提的导出需求';")
PULSE_HOME="$E/p2" $P requirement update "$RID" --status in_dev | grep -q '需求已更新' || fail "p2 requirement update"
PULSE_HOME="$E/p1" $P sync --project demo | tee "$E/sync_back.out"
grep -q '同步完成' "$E/sync_back.out" || fail "p1 回拉 sync"
grep -q '代理提的导出需求 被飞书侧更新覆盖' "$E/sync_back.out" || fail "p1 应输出六实体覆盖警告"
s1=$(sqlite3 "$E/p1/pulse.db" "SELECT status FROM requirements WHERE title='代理提的导出需求';")
s2=$(sqlite3 "$E/p2/pulse.db" "SELECT status FROM requirements WHERE title='代理提的导出需求';")
[ "$s1" = "$s2" ] || fail "双机需求状态不一致: p1=$s1 p2=$s2"
[ "$s1" = "in_dev" ] || fail "p1 未收敛 p2 的状态修改: $s1"

echo "SETUP V11 DONE (全链 + 8 表 bind/sync + 模板文档 + MCP 归因 + 双机六表收敛)"
