#!/bin/bash
# 可重放 E2E 环境搭建: 重建仿真飞书 + 双机 HOME + 基础数据 + bind + 首次 sync
set -e
P=/Users/demo/my_project/lacus/pulse/pulse
pkill -f fakefeishu 2>/dev/null || true
sleep 0.3
cd /tmp/pulse-e2e/fakefeishu && go build -o fakefeishu . && (nohup ./fakefeishu > /tmp/pulse-e2e/fake.log 2>&1 &)
sleep 0.8
rm -rf /tmp/pulse-e2e/p1 /tmp/pulse-e2e/p2
mkdir -p /tmp/pulse-e2e/p1 /tmp/pulse-e2e/p2
printf 'default_actor: zhangsan\nfeishu:\n  app_id: cli_fake\n  app_secret: fake_secret\nsync:\n  stale_minutes: 10\n' > /tmp/pulse-e2e/p1/config.yaml
printf 'default_actor: lisi\nfeishu:\n  app_id: cli_fake\n  app_secret: fake_secret\nsync:\n  stale_minutes: 10\n' > /tmp/pulse-e2e/p2/config.yaml

export PULSE_HOME=/tmp/pulse-e2e/p1 PULSE_FEISHU_ENDPOINT=http://127.0.0.1:19090
$P init demo --name 演示项目 >/dev/null
$P member add zhangsan >/dev/null
$P member add lisi >/dev/null
$P member add codex --type agent >/dev/null
$P version add v1.0 --project demo --target 2026-09-30 >/dev/null
$P task add '登录接口开发' --project demo --assignee zhangsan --estimate 3 --start 2026-09-10 --due 2026-09-18 --version v1.0 >/dev/null
$P task add '登录接口自测' --project demo --assignee zhangsan --estimate 1 --start 2026-09-19 --due 2026-09-20 --version v1.0 >/dev/null
$P task add '会议纪要整理' --project demo --estimate 1 --due 2026-09-13 >/dev/null
$P task dep 2 --on 1 >/dev/null
$P task update 1 --status done >/dev/null
$P task update 1 --status in_progress >/dev/null   # reopen 语义
$P feishu bind --project demo | head -2
$P sync --project demo
echo "SETUP DONE (p1 就绪, base 已建, 首次 sync 完成)"
