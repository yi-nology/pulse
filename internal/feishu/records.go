// records.go 实现协作记录文档模板（spec §3.2）：pulse 持有每条记录的结构化字段，
// 需要多人协作填写的内容（需求长文、评审结论、会议纪要、提测自检清单、发版清单）
// 由 pulse 按内置模板创建飞书文档并把 token 回填实体行。核心入口 EnsureRecordDoc
// 是 get-or-create 语义：
//   - 实体已有 token → 原样返回（零飞书调用）；
//   - 否则 DocCreate（标题按模板）→ BlockAppend（模板块，自实体当前数据渲染，
//     负责人/版本经 store 查询换名，查不到显示占位 id）→ store.SetRecordDocToken
//     回写（同一事务内落 activity action="feishu_record_doc"）。
//
// 文档内容永不回流：pulse 只建一次，不更新、不解析，状态流转只经 CLI/MCP 显式操作。
// 未配置飞书时调用方传 nil client，本函数返回引导性错误，由调用方降级为
// "已保存记录（无文档）" 提示（该实体仍可无文档使用）。
package feishu

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// errFeishuNotConfigured 是未配置飞书凭据时的引导性错误（c 为 nil 即视为未配置）。
var errFeishuNotConfigured = errors.New("未配置飞书：请先在 $PULSE_HOME/config.yaml 配置 feishu.app_id/app_secret（或设置环境变量 PULSE_FEISHU_APP_SECRET）")

// recordDoc 是一次待创建文档的渲染结果；token 非空表示实体已绑定（无需创建）。
type recordDoc struct {
	title  string
	blocks []map[string]any
	token  string
}

// EnsureRecordDoc 确保协作记录实体（requirement|review|meeting|test_submission|release）
// 绑定了模板文档：已有 token 直接返回；否则建 doc + 模板块 + 回写 token。
// actorName 用于活动归属（经 actor 解析，get-or-create）。
func EnsureRecordDoc(ctx context.Context, c *Client, s *store.Store, entityKind string, id int64, actorName string) (string, error) {
	if c == nil {
		return "", errFeishuNotConfigured
	}
	doc, found, err := loadRecordDoc(s, entityKind, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%s不存在: id=%d", recordLabel(entityKind), id)
	}
	if doc.token != "" { // 已绑定：幂等返回，零 API 调用
		return doc.token, nil
	}
	a, behalf, err := actor.Resolve(s, actorName, "", "")
	if err != nil {
		return "", err
	}
	docToken, err := c.API().DocCreate(ctx, "", doc.title)
	if err != nil {
		return "", fmt.Errorf("创建协作记录文档失败: %w", err)
	}
	if err := c.API().BlockAppend(ctx, docToken, doc.blocks); err != nil {
		return "", fmt.Errorf("写入模板块失败（%s）: %w", doc.title, err)
	}
	if err := s.SetRecordDocToken(entityKind, id, docToken, a, behalf); err != nil {
		return "", fmt.Errorf("写回文档 token 失败: %w", err)
	}
	return docToken, nil
}

// loadRecordDoc 加载实体并渲染模板；found=false 表示实体不存在。已绑定 token 的实体
// 只回填 token、不渲染块（省去人名/版本查询）。
func loadRecordDoc(s *store.Store, entityKind string, id int64) (recordDoc, bool, error) {
	switch entityKind {
	case "requirement":
		r, found, err := s.GetRequirement(id)
		if err != nil || !found {
			return recordDoc{}, found, err
		}
		if r.FeishuDocToken != "" {
			return recordDoc{token: r.FeishuDocToken}, true, nil
		}
		blocks, err := requirementDocBlocks(s, r)
		if err != nil {
			return recordDoc{}, true, err
		}
		return recordDoc{title: "需求 · " + r.Title, blocks: blocks}, true, nil
	case "review":
		v, found, err := s.GetReview(id)
		if err != nil || !found {
			return recordDoc{}, found, err
		}
		if v.FeishuDocToken != "" {
			return recordDoc{token: v.FeishuDocToken}, true, nil
		}
		return recordDoc{title: reviewDocTitle(v), blocks: reviewDocBlocks(v)}, true, nil
	case "meeting":
		m, found, err := s.GetMeeting(id)
		if err != nil || !found {
			return recordDoc{}, found, err
		}
		if m.FeishuDocToken != "" {
			return recordDoc{token: m.FeishuDocToken}, true, nil
		}
		return recordDoc{title: "会议纪要 · " + m.Title, blocks: meetingDocBlocks(m)}, true, nil
	case "test_submission":
		t, found, err := s.GetTestSubmission(id)
		if err != nil || !found {
			return recordDoc{}, found, err
		}
		if t.FeishuDocToken != "" {
			return recordDoc{token: t.FeishuDocToken}, true, nil
		}
		title, err := submissionDocTitle(s, t)
		if err != nil {
			return recordDoc{}, true, err
		}
		blocks, err := submissionDocBlocks(s, t)
		if err != nil {
			return recordDoc{}, true, err
		}
		return recordDoc{title: title, blocks: blocks}, true, nil
	case "release":
		r, found, err := s.GetRelease(id)
		if err != nil || !found {
			return recordDoc{}, found, err
		}
		if r.FeishuDocToken != "" {
			return recordDoc{token: r.FeishuDocToken}, true, nil
		}
		title, err := releaseDocTitle(s, r)
		if err != nil {
			return recordDoc{}, true, err
		}
		names, err := memberNameMap(s)
		if err != nil {
			return recordDoc{}, true, err
		}
		return recordDoc{title: title, blocks: releaseDocBlocks(names, r)}, true, nil
	default:
		return recordDoc{}, false, fmt.Errorf("未知实体类型 %q（须为 requirement|review|meeting|test_submission|release）", entityKind)
	}
}

// recordLabel 实体的中文名（错误信息用，与 store 层文案一致）。
func recordLabel(entityKind string) string {
	switch entityKind {
	case "requirement":
		return "需求"
	case "review":
		return "评审"
	case "meeting":
		return "会议"
	case "test_submission":
		return "提测单"
	case "release":
		return "发版"
	}
	return entityKind
}

// —— 名称渲染辅助（查不到显示占位 id）—————————————————————————

// memberNameMap 全量成员 ID→名映射（成员为全局小表，协作记录模板的人名渲染用）。
func memberNameMap(s *store.Store) (map[int64]string, error) {
	ms, err := s.ListMembers()
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(ms))
	for _, m := range ms {
		names[m.ID] = m.Name
	}
	return names, nil
}

// displayName 渲染成员引用：0 → 未指定；查不到 → 成员#<id>。
func displayName(names map[int64]string, id int64) string {
	if id == 0 {
		return "未指定"
	}
	if n, ok := names[id]; ok {
		return n
	}
	return fmt.Sprintf("成员#%d", id)
}

// versionDisplayName 渲染版本引用：0 → 未指定；查不到 → 版本#<id>；否则版本名。
func versionDisplayName(s *store.Store, id int64) (string, error) {
	if id == 0 {
		return "未指定", nil
	}
	v, found, err := s.GetVersion(id)
	if err != nil {
		return "", err
	}
	if !found {
		return fmt.Sprintf("版本#%d", id), nil
	}
	return v.Name, nil
}

// —— 五套模板（heading2 节 + bullet 字段行 + todo 清单）——————————————————

// requirementDocBlocks 需求文档：背景与描述、状态与负责人、关联任务提示。
func requirementDocBlocks(s *store.Store, r model.Requirement) ([]map[string]any, error) {
	names, err := memberNameMap(s)
	if err != nil {
		return nil, err
	}
	desc := r.Description
	if desc == "" {
		desc = "（协作填写：背景与描述；短需求可直接维护在 pulse 的 description 字段）"
	}
	return []map[string]any{
		headingBlock("背景与描述"),
		bulletBlock(desc),
		headingBlock("状态与负责人"),
		bulletBlock("状态：" + r.Status),
		bulletBlock("负责人：" + displayName(names, r.OwnerID)),
		bulletBlock(fmt.Sprintf("优先级：%d", r.Priority)),
		headingBlock("关联任务提示"),
		bulletBlock("行动项需跟踪时可用 pulse task add 建任务"),
	}, nil
}

// reviewDocTitle 评审纪要标题：`评审记录 · <kind> · 需求#<id|->`（未关联需求显示 -）。
func reviewDocTitle(v model.Review) string {
	req := "-"
	if v.RequirementID != 0 {
		req = strconv.FormatInt(v.RequirementID, 10)
	}
	return fmt.Sprintf("评审记录 · %s · 需求#%s", v.Kind, req)
}

// reviewDocBlocks 评审纪要：参与人与时间、结论（当前值占位，流转仍只经 CLI/MCP）、
// 行动项 todo。
func reviewDocBlocks(v model.Review) []map[string]any {
	return []map[string]any{
		headingBlock("参与人与时间"),
		bulletBlock("时间：" + v.HeldAt),
		bulletBlock("参与人：（协作填写）"),
		headingBlock("结论"),
		bulletBlock("结论：" + v.Conclusion),
		headingBlock("行动项"),
		todoBlock("（协作填写：评审行动项；需跟踪时可用 pulse task add 建任务）"),
	}
}

// meetingDocBlocks 会议纪要：时间/参会人、讨论要点（bullet 占位）、行动项 todo。
func meetingDocBlocks(m model.Meeting) []map[string]any {
	return []map[string]any{
		headingBlock("时间/参会人"),
		bulletBlock("时间：" + m.HeldAt),
		bulletBlock("参会人：（协作填写）"),
		headingBlock("讨论要点"),
		bulletBlock("（协作填写：讨论要点）"),
		headingBlock("行动项"),
		todoBlock("（协作填写：会议行动项；需跟踪时可用 pulse task add 建任务）"),
	}
}

// submissionDocTitle 提测单标题：`提测单 · <version名> · #<id>`。
func submissionDocTitle(s *store.Store, t model.TestSubmission) (string, error) {
	version, err := versionDisplayName(s, t.VersionID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("提测单 · %s · #%d", version, t.ID), nil
}

// submissionDocBlocks 提测单：提测人/测试负责人、范围（scope）、自检清单（固定三项
// todo——清单内容在飞书协作，pulse 不持有）、风险。
func submissionDocBlocks(s *store.Store, t model.TestSubmission) ([]map[string]any, error) {
	names, err := memberNameMap(s)
	if err != nil {
		return nil, err
	}
	scope := t.Scope
	if scope == "" {
		scope = "（协作填写：提测范围）"
	}
	return []map[string]any{
		headingBlock("提测人/测试负责人"),
		bulletBlock("提测人：" + displayName(names, t.SubmittedBy)),
		bulletBlock("测试负责人：" + displayName(names, t.TestOwnerID)),
		headingBlock("范围"),
		bulletBlock(scope),
		headingBlock("自检清单"),
		todoBlock("冒烟通过"),
		todoBlock("核心路径回归"),
		todoBlock("数据兼容"),
		headingBlock("风险"),
		bulletBlock("（协作填写：风险与注意事项）"),
	}, nil
}

// releaseDocTitle 发版记录标题：`发版记录 · <version名>`。
func releaseDocTitle(s *store.Store, r model.Release) (string, error) {
	version, err := versionDisplayName(s, r.VersionID)
	if err != nil {
		return "", err
	}
	return "发版记录 · " + version, nil
}

// releaseDocBlocks 发版记录：发布负责人、检查清单（固定三项 todo）、状态与时间。
func releaseDocBlocks(names map[int64]string, r model.Release) []map[string]any {
	releasedAt := r.ReleasedAt
	if releasedAt == "" {
		releasedAt = "未发布"
	}
	return []map[string]any{
		headingBlock("发布负责人"),
		bulletBlock("发布负责人：" + displayName(names, r.ReleaseManagerID)),
		headingBlock("检查清单"),
		todoBlock("测试通过"),
		todoBlock("回滚方案确认"),
		todoBlock("发布公告"),
		headingBlock("状态与时间"),
		bulletBlock("状态：" + r.Status),
		bulletBlock("发布时间：" + releasedAt),
	}
}
