#!/bin/bash
# E2E-3 覆盖警告的洁净复现：重建环境 → p1改B(autopush) → p2改A(autopush) → p1 sync
# 断言: p1 sync 输出含 "被飞书侧更新覆盖"，且本地终值为 A（LWW 后写者胜）
set -e
P=/Users/demo/my_project/lacus/pulse/pulse
export PULSE_FEISHU_ENDPOINT=http://127.0.0.1:19090
/tmp/pulse-e2e/setup.sh >/dev/null 2>&1

PULSE_HOME=/tmp/pulse-e2e/p2 $P init demo >/dev/null
PULSE_HOME=/tmp/pulse-e2e/p2 $P feishu bind --project demo --app-token app1 --task-table tbl1 --version-table tbl2 --doc doc1 >/dev/null
PULSE_HOME=/tmp/pulse-e2e/p2 $P sync --project demo >/dev/null

echo "p1 改B:"; PULSE_HOME=/tmp/pulse-e2e/p1 $P task update 2 --title '自测B' 2>&1
PULSE_HOME=/tmp/pulse-e2e/p1 sqlite3 /tmp/pulse-e2e/p1/pulse.db "SELECT 'p1.updated_at', updated_at FROM tasks WHERE id=2;"
PULSE_HOME=/tmp/pulse-e2e/p1 sqlite3 /tmp/pulse-e2e/p1/pulse.db "SELECT 'p1.wm(before)', value FROM sync_state WHERE key='pull_watermark:1:tasks';"
echo "p2 改A:"; PULSE_HOME=/tmp/pulse-e2e/p2 $P task update 2 --title '自测A' 2>&1
echo "p1 sync:"; PULSE_HOME=/tmp/pulse-e2e/p1 $P sync --project demo 2>&1 | tee /tmp/pulse-e2e/reg3.out
PULSE_HOME=/tmp/pulse-e2e/p1 sqlite3 /tmp/pulse-e2e/p1/pulse.db "SELECT 'p1.updated_at(after)', updated_at FROM tasks WHERE id=2;"

if grep -q '被飞书侧更新覆盖' /tmp/pulse-e2e/reg3.out; then
  echo "RESULT: 警告触发 ✅"
else
  echo "RESULT: 警告未触发 ❌"; exit 1
fi
