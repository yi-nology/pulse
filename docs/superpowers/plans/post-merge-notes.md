# Pulse MVP 合并后待办（源自最终全分支审查 triage）

> 2026-09-15，最终审查结论 With fixes → 修复波 eef07fe 后收口。以下为审查裁定 DEFER 的快速改进项与真实使用前置条件。

## 建议尽快做（小改动）—— ✅ 已全部完成（2026-09-15 打磨波+遗留批次 ee83ed9..a2c610c）

1. ~~rules 常量导出~~ ✅ ee83ed9
2. ~~dependencies 唯一索引~~ ✅ ee83ed9（idx_dependencies_pair + ErrDuplicateDependency 映射）
3. ~~AutopushFunc 归因 agent~~ ✅ 001a658（BestEffort/AutopushFunc 携带 agentName）
4. ~~README 已知限制~~ ✅ ee83ed9 + 后续补充（列类型升级/协作文档分叉/需求ID 错链）
5. ~~.gitignore `/pulse`~~ ✅ ee83ed9

另已完成的原 v1.2 候选：tasks `--requirement` 写路径（a33814d，模板文案同步回改）、conclude_review/create_meeting MCP（4d273f7）、REAL-2 create 超时防重复（2ba05e8 + a2c610c 归一化修复）、网络超时补试（f86e5b8）、BlockAppend 失败回写 token 缓解孤儿（8955b9c）、Mimosa 完整深度扫描 0 发现（scan-2026-09-15T14-33-51，seal sha256:1898d2c9…）。

## 真实使用前必须人工过一遍

- **执行 docs/superpowers/plans/e2e-checklist.md 全部 6 场景**（需要真实飞书测试租户）：bind/sync/publish 目前只经 httptest 验证，bind 部分失败的孤儿 base 行为、甘特视图手动建/改列步骤需要一次真人确认。
- 飞书权限 scope 名（bitable:app / docx:document / drive:file）在开放平台控制台开通时逐字核对。
- 双机场景（checklist 场景 4）用 `pulse feishu bind --app-token …` 采用模式实测收敛。

## 审查裁定记录（DEFER 摘要）

- 全部 14 个任务的 task-scoped minor 项均裁定 DEFER（详见 SDD ledger，工作区已删，裁定理由在 git 提交历史与各任务审查中）。
- parked 的 dependencies TOCTOU：单事务内 check-then-insert，本地单用户 CLI 风险极小，最坏情况重复一条依赖行（仅甘特文字标注重复）→ 上面第 2 条关闭它。
- warnWriter 仍是包级全局（测试注入用）：MCP 已改为 set-once，但未来若在工具回调里再 Set 会重新引入竞争——加注释警惕即可。
