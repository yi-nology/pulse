// mapping_v11.go 定义 v1.1 研发交付闭环六实体与 Bitable 字段的双向映射。
// 约定与 mapping.go 的任务表完全一致（brief §6）：
//   - 往返一致（XToFields ∘ FieldsToX = id）是自回声判定的根基，两端必须产出
//     完全相同的键集合与 JSON 类型（文本列 string、复选框 bool）；
//   - 远端缺必填字段 → missing 非空（整条跳过并告警）；未知字段 → 告警忽略；
//   - updated_by 列 bind 时创建但不参与同步（不计入指纹），远端出现时静默忽略；
//   - 人员列（负责人/提测人/测试负责人/发布负责人）按成员名解析，查不到保留原值并告警
//     （sync 侧已对远端提到的人名先行 get-or-create）；版本列（发现版本/版本）按版本名解析；
//   - 需求引用列（评审/bug/提测 的 需求ID）存需求的**全局 UID**（v1.2）：pull 侧按
//     UID 解析回本地 id，旧 base 的本地 id 数字串容忍解析一次并告警迁移；
//   - 评审的 评审类型/评审时间/需求ID、会议的 会议标题/时间、发版的 发布时间 为
//     "创建即定"字段：store 无对应更新路径，base 已有值时保留本地值、远端不一致仅告警；
//     base 为零值（合入全新远端行）时采用远端值。
package feishu

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zhangyi/pulse/internal/model"
)

// —— 需求 ————————————————————————————————————————————————————————————————

// RequirementToFields 把本地需求映射为 Bitable 字段（与 bind 建表列对应）。
// 需求UID 是全局身份（v1.2）：随记录同步，跨机引用按它解析；空值省略键
// （旧库行由同步路径回填后再推送）。
func RequirementToFields(r model.Requirement, memberNameByID map[int64]string) map[string]any {
	fields := map[string]any{
		"需求名": r.Title,
		"状态":  r.Status,
		"负责人": memberNameByID[r.OwnerID],
		"优先级": strconv.Itoa(r.Priority),
		"描述":  r.Description,
		"已废弃": r.Archived,
	}
	if r.UID != "" {
		fields["需求UID"] = r.UID
	}
	if d := collabDocField(r.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var requirementRequiredFields = []string{"需求名", "状态", "优先级"}

var requirementFieldSet = map[string]bool{
	"需求名": true, "状态": true, "负责人": true, "优先级": true, "描述": true, "已废弃": true,
	"需求UID": true, "协作文档": true,
}

// FieldsToRequirement 把远端字段映射到本地需求的变更（local 提供未映射字段的底值，
// 如 Source 本地独有、Archived 不被远端内容覆盖）。需求UID 非空时随远端合入
// （双机各自生成的行随共享记录收敛为同一全局身份），键缺失/为空时保留本地值。
func FieldsToRequirement(f map[string]any, local model.Requirement, memberIDByName map[string]int64) (changed model.Requirement, missing []string, warnings []string) {
	changed = local
	for _, k := range requirementRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Title = toText(f["需求名"])
	changed.Status = toText(f["状态"])
	if v, ok := f["需求UID"]; ok {
		if uid := toText(v); uid != "" {
			changed.UID = uid
		}
	}
	if v, ok := f["负责人"]; ok {
		name := toText(v)
		if name == "" {
			changed.OwnerID = 0
		} else if id, ok := memberIDByName[name]; ok {
			changed.OwnerID = id
		} else {
			warnings = append(warnings, fmt.Sprintf("负责人 %q 不在本地成员表，保留原负责人", name))
		}
	}
	if v, ok := f["优先级"]; ok {
		if n, err := strconv.Atoi(toText(v)); err == nil {
			changed.Priority = n
		} else {
			warnings = append(warnings, fmt.Sprintf("优先级 %q 无法解析为整数，保留原值", toText(v)))
		}
	}
	if v, ok := f["描述"]; ok {
		changed.Description = toText(v)
	}
	warnings = append(warnings, unknownFieldWarnings(f, requirementFieldSet)...)
	return changed, nil, warnings
}

// —— 评审 ————————————————————————————————————————————————————————————————

// ReviewToFields 把本地评审映射为 Bitable 字段；需求ID 列内容是需求的**全局 UID**
// （v1.2 起修复跨机错链：本地自增 id 只在本机唯一，双机各自建需求会撞号）。
func ReviewToFields(v model.Review, reqIDToUID map[int64]string) map[string]any {
	fields := map[string]any{
		"评审类型": v.Kind,
		"结论":   v.Conclusion,
		"需求ID": reqIDToUID[v.RequirementID],
		"已废弃":  v.Archived,
	}
	if c := dateToCell(v.HeldAt); c != nil {
		fields["评审时间"] = c
	}
	if d := collabDocField(v.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var reviewRequiredFields = []string{"评审类型", "结论"}

var reviewFieldSet = map[string]bool{
	"评审类型": true, "结论": true, "评审时间": true, "需求ID": true, "已废弃": true,
	"协作文档": true,
}

// FieldsToReview 把远端字段映射到本地评审的变更；仅 结论 可远端合入（sync 侧经
// UpdateReviewConclusion 落库并落 update 活动），评审类型/评审时间/需求ID 创建即定。
// 需求ID 列按全局 UID 解析回本地 id（旧 base 的数字串容忍解析一次并告警迁移），
// 查不到时保留本地关联并告警。
func FieldsToReview(f map[string]any, local model.Review, reqUIDToID map[string]int64) (changed model.Review, missing []string, warnings []string) {
	changed = local
	for _, k := range reviewRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Kind = adoptImmutable(local.Kind, toText(f["评审类型"]), "评审类型", &warnings)
	changed.Conclusion = toText(f["结论"])
	changed.HeldAt = adoptImmutable(local.HeldAt, cellToDate(f["评审时间"]), "评审时间", &warnings)
	if v, ok := f["需求ID"]; ok {
		text := toText(v)
		if text == "" {
			if local.RequirementID == 0 {
				changed.RequirementID = 0
			} else {
				warnings = append(warnings, "远端需求ID 为空，保留本地关联")
			}
		} else {
			changed.RequirementID = adoptImmutableID(local.RequirementID,
				resolveRequirementRef(text, local.RequirementID, reqUIDToID, &warnings),
				"需求ID", &warnings)
		}
	}
	warnings = append(warnings, unknownFieldWarnings(f, reviewFieldSet)...)
	return changed, nil, warnings
}

// —— 会议 ————————————————————————————————————————————————————————————————

// MemberToFields 把本地成员映射为飞书"成员表"字段（单向镜像：姓名/类型/周容量/备注；
// 名单与容量供人在飞书侧查看，成员维护走 CLI/MCP，飞书侧修改不回流）。
func MemberToFields(m model.Member) map[string]any {
	return map[string]any{
		"姓名":    m.Name,
		"类型":    m.Type,
		"周容量人日": m.Capacity,
		"备注":    m.Notes,
	}
}

// MeetingToFields 把本地会议映射为 Bitable 字段。
func MeetingToFields(m model.Meeting) map[string]any {
	fields := map[string]any{
		"会议标题": m.Title,
		"已废弃":  m.Archived,
	}
	if c := dateToCell(m.HeldAt); c != nil {
		fields["时间"] = c
	}
	if d := collabDocField(m.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var meetingRequiredFields = []string{"会议标题"}

var meetingFieldSet = map[string]bool{"会议标题": true, "时间": true, "已废弃": true, "协作文档": true}

// FieldsToMeeting 把远端字段映射到本地会议的变更；会议标题/时间创建即定
// （会议无更新路径，sync 侧仅建行与墓碑，远端修改吸收不回流）。
func FieldsToMeeting(f map[string]any, local model.Meeting) (changed model.Meeting, missing []string, warnings []string) {
	changed = local
	for _, k := range meetingRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Title = adoptImmutable(local.Title, toText(f["会议标题"]), "会议标题", &warnings)
	if v, ok := f["时间"]; ok {
		changed.HeldAt = adoptImmutable(local.HeldAt, cellToDate(v), "时间", &warnings)
	}
	warnings = append(warnings, unknownFieldWarnings(f, meetingFieldSet)...)
	return changed, nil, warnings
}

// —— bug —————————————————————————————————————————————————————————————————

// BugToFields 把本地 bug 映射为 Bitable 字段（严重级 1..4 落文本列；需求ID 列为
// 需求的全局 UID，v1.2 起随记录同步）。
func BugToFields(b model.Bug, memberNameByID, versionNameByID map[int64]string, reqIDToUID map[int64]string) map[string]any {
	fields := map[string]any{
		"标题":   b.Title,
		"严重级":  strconv.Itoa(b.Severity),
		"状态":   b.Status,
		"负责人":  memberNameByID[b.AssigneeID],
		"发现版本": versionNameByID[b.FoundVersionID],
		"需求ID": reqIDToUID[b.RequirementID],
		"已废弃":  b.Archived,
	}
	if d := collabDocField(b.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var bugRequiredFields = []string{"标题", "严重级", "状态"}

var bugFieldSet = map[string]bool{
	"标题": true, "严重级": true, "状态": true, "负责人": true, "发现版本": true, "已废弃": true,
	"需求ID": true, "协作文档": true,
}

// FieldsToBug 把远端字段映射到本地 bug 的变更（Description/FixTaskID 本地独有，不被
// 远端内容覆盖）。需求ID 列按全局 UID 解析（bug 的需求引用可随远端更新，
// store 的 UpdateBug 有对应通道）；查不到保留本地关联并告警。
func FieldsToBug(f map[string]any, local model.Bug, memberIDByName, versionIDByName map[string]int64, reqUIDToID map[string]int64) (changed model.Bug, missing []string, warnings []string) {
	changed = local
	for _, k := range bugRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Title = toText(f["标题"])
	changed.Status = toText(f["状态"])
	if v, ok := f["严重级"]; ok {
		if n, err := strconv.Atoi(toText(v)); err == nil && n >= 1 && n <= 4 {
			changed.Severity = n
		} else {
			warnings = append(warnings, fmt.Sprintf("严重级 %q 无法解析为 1..4，保留原值", toText(v)))
		}
	}
	if v, ok := f["负责人"]; ok {
		name := toText(v)
		if name == "" {
			changed.AssigneeID = 0
		} else if id, ok := memberIDByName[name]; ok {
			changed.AssigneeID = id
		} else {
			warnings = append(warnings, fmt.Sprintf("负责人 %q 不在本地成员表，保留原负责人", name))
		}
	}
	if v, ok := f["发现版本"]; ok {
		vname := toText(v)
		if vname == "" {
			changed.FoundVersionID = 0
		} else if vid, ok := versionIDByName[vname]; ok {
			changed.FoundVersionID = vid
		} else {
			warnings = append(warnings, fmt.Sprintf("版本 %q 不在本地版本表，保留原版本", vname))
		}
	}
	if v, ok := f["需求ID"]; ok {
		text := toText(v)
		if text == "" {
			if local.RequirementID == 0 {
				changed.RequirementID = 0
			} else {
				warnings = append(warnings, "远端需求ID 为空，保留本地关联")
			}
		} else {
			changed.RequirementID = resolveRequirementRef(text, local.RequirementID, reqUIDToID, &warnings)
		}
	}
	warnings = append(warnings, unknownFieldWarnings(f, bugFieldSet)...)
	return changed, nil, warnings
}

// —— 提测单 ————————————————————————————————————————————————————————————————

// SubmissionToFields 把本地提测单映射为 Bitable 字段（需求ID 列为需求的全局 UID）。
func SubmissionToFields(t model.TestSubmission, memberNameByID, versionNameByID map[int64]string, reqIDToUID map[int64]string) map[string]any {
	fields := map[string]any{
		"版本":    versionNameByID[t.VersionID],
		"状态":    t.Status,
		"提测人":   memberNameByID[t.SubmittedBy],
		"测试负责人": memberNameByID[t.TestOwnerID],
		"范围":    t.Scope,
		"需求ID":  reqIDToUID[t.RequirementID],
		"已废弃":   t.Archived,
	}
	if d := collabDocField(t.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var submissionRequiredFields = []string{"版本", "状态"}

var submissionFieldSet = map[string]bool{
	"版本": true, "状态": true, "提测人": true, "测试负责人": true, "范围": true, "已废弃": true,
	"需求ID": true, "协作文档": true,
}

// FieldsToSubmission 把远端字段映射到本地提测单的变更（需求ID 创建即定——store 无对应
// 更新通道，base 已有值时保留本地、远端不一致仅告警；submitted_at/concluded_at 为
// store 派生值，不随远端内容覆盖）。需求ID 列按全局 UID 解析回本地 id。
func FieldsToSubmission(f map[string]any, local model.TestSubmission, memberIDByName, versionIDByName map[string]int64, reqUIDToID map[string]int64) (changed model.TestSubmission, missing []string, warnings []string) {
	changed = local
	for _, k := range submissionRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Status = toText(f["状态"])
	if v, ok := f["版本"]; ok {
		vname := toText(v)
		if vname == "" {
			changed.VersionID = 0
		} else if vid, ok := versionIDByName[vname]; ok {
			changed.VersionID = vid
		} else {
			warnings = append(warnings, fmt.Sprintf("版本 %q 不在本地版本表，保留原版本", vname))
		}
	}
	changed.SubmittedBy = resolveMemberFromField(f, "提测人", local.SubmittedBy, memberIDByName, &warnings)
	changed.TestOwnerID = resolveMemberFromField(f, "测试负责人", local.TestOwnerID, memberIDByName, &warnings)
	if v, ok := f["范围"]; ok {
		changed.Scope = toText(v)
	}
	if v, ok := f["需求ID"]; ok {
		text := toText(v)
		if text == "" {
			if local.RequirementID == 0 {
				changed.RequirementID = 0
			} else {
				warnings = append(warnings, "远端需求ID 为空，保留本地关联")
			}
		} else {
			changed.RequirementID = adoptImmutableID(local.RequirementID,
				resolveRequirementRef(text, local.RequirementID, reqUIDToID, &warnings),
				"需求ID", &warnings)
		}
	}
	warnings = append(warnings, unknownFieldWarnings(f, submissionFieldSet)...)
	return changed, nil, warnings
}

// —— 发版记录 ———————————————————————————————————————————————————————————————

// ReleaseToFields 把本地发版记录映射为 Bitable 字段。
func ReleaseToFields(r model.Release, memberNameByID, versionNameByID map[int64]string) map[string]any {
	fields := map[string]any{
		"版本":    versionNameByID[r.VersionID],
		"状态":    r.Status,
		"发布负责人": memberNameByID[r.ReleaseManagerID],
		"备注":    r.Notes,
		"已废弃":   r.Archived,
	}
	if c := dateToCell(r.ReleasedAt); c != nil {
		fields["发布时间"] = c
	}
	if d := collabDocField(r.FeishuDocToken); d != nil {
		fields["协作文档"] = d
	}
	return fields
}

var releaseRequiredFields = []string{"版本", "状态"}

var releaseFieldSet = map[string]bool{
	"版本": true, "状态": true, "发布负责人": true, "发布时间": true, "备注": true, "已废弃": true,
	"协作文档": true,
}

// FieldsToRelease 把远端字段映射到本地发版的变更；发布时间创建即定（store 在进入
// released 时自动补记，不随远端内容覆盖）。
func FieldsToRelease(f map[string]any, local model.Release, memberIDByName, versionIDByName map[string]int64) (changed model.Release, missing []string, warnings []string) {
	changed = local
	for _, k := range releaseRequiredFields {
		if _, ok := f[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return local, missing, nil
	}
	changed.Status = toText(f["状态"])
	if v, ok := f["版本"]; ok {
		vname := toText(v)
		if vname == "" {
			changed.VersionID = 0
		} else if vid, ok := versionIDByName[vname]; ok {
			changed.VersionID = vid
		} else {
			warnings = append(warnings, fmt.Sprintf("版本 %q 不在本地版本表，保留原版本", vname))
		}
	}
	changed.ReleaseManagerID = resolveMemberFromField(f, "发布负责人", local.ReleaseManagerID, memberIDByName, &warnings)
	if v, ok := f["发布时间"]; ok {
		changed.ReleasedAt = adoptImmutable(local.ReleasedAt, cellToDate(v), "发布时间", &warnings)
	}
	if v, ok := f["备注"]; ok {
		changed.Notes = toText(v)
	}
	warnings = append(warnings, unknownFieldWarnings(f, releaseFieldSet)...)
	return changed, nil, warnings
}

// —— 共用小工具 ————————————————————————————————————————————————————————————

// docURLPrefix 是"协作文档"列的链接前缀（docx 新版文档）。
const docURLPrefix = "https://www.feishu.cn/docx/"

// collabDocField 把非空 doc token 映射为"协作文档"超链接列的写入形态；
// 空 token 返回 nil（调用方省略该键，不向远端写空链接）。
func collabDocField(docToken string) map[string]any {
	if docToken == "" {
		return nil
	}
	return map[string]any{"text": "打开文档", "link": docURLPrefix + docToken}
}

// collabDocToken 从远端"协作文档"列提取 doc token（取链接 path 的最后一段）。
// 形态容错：裸链接/token 字符串、{"text","link"} 对象或其数组（取首元素）；
// 解析不出返回空串（调用方跳过采纳）。
func collabDocToken(v any) string {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return ""
		}
		return collabDocToken(x[0])
	case map[string]any:
		if link, ok := x["link"].(string); ok && link != "" {
			return docTokenFromURL(link)
		}
		if text, ok := x["text"].(string); ok {
			return docTokenFromURL(text)
		}
		return ""
	case string:
		return docTokenFromURL(x)
	default:
		return ""
	}
}

// docTokenFromURL 取 URL path 最后一段作为 doc token；容忍直接存 token 的形态
// （无斜杠按整串处理）；查询串/锚点剥离；非 ASCII 字母数字（飞书 token 字符集外，
// 如手填的杂讯）返回 ""。
func docTokenFromURL(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return s
}

// resolveRequirementRef 把远端 需求ID 列文本解析回本地需求 id（v1.2 全局身份）：
// 优先按需求UID 解析（32 位十六进制，跨机唯一）；未命中且为纯数字时按旧版本地 id
// 容忍解析一次并告警提示迁移（push 以 UID 覆盖后一轮收敛）；两者皆不中时保留原
// 关联并告警（对齐"版本不在本地"的容错风格）。
func resolveRequirementRef(text string, local int64, reqUIDToID map[string]int64, warnings *[]string) int64 {
	if id, ok := reqUIDToID[text]; ok {
		return id
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		*warnings = append(*warnings, fmt.Sprintf("需求ID %q 为旧本地 id 格式，已按本地 id 解析（同步一轮后将迁移为需求UID）", text))
		return n
	}
	*warnings = append(*warnings, fmt.Sprintf("需求ID %q 不在本地需求表（既非需求UID也非本地id），保留原关联", text))
	return local
}

// adoptImmutable 是"创建即定"文本字段的合入规则：base 为空（合入全新远端行）时采用
// 远端值；base 已有值时保留本地值，远端给出不同值仅告警（store 无该字段的更新路径）。
func adoptImmutable(base, remote, field string, warnings *[]string) string {
	if base == "" {
		return remote
	}
	if remote != "" && remote != base {
		*warnings = append(*warnings, fmt.Sprintf("%s %q 为创建即定字段，保留本地值 %q", field, remote, base))
	}
	return base
}

// adoptImmutableID 是"创建即定"引用列（需求ID）的合入规则，语义同 adoptImmutable。
func adoptImmutableID(base, remote int64, field string, warnings *[]string) int64 {
	if base == 0 {
		return remote
	}
	if remote != 0 && remote != base {
		*warnings = append(*warnings, fmt.Sprintf("%s #%d 为创建即定字段，保留本地关联 #%d", field, remote, base))
	}
	return base
}

// resolveMemberFromField 把远端人员列解析为成员 ID：空值清空；已知名解析；未知名
// 保留原值并告警（sync 侧已对远端提到的人名先行 get-or-create，此处兜底防御）。
func resolveMemberFromField(f map[string]any, column string, local int64, memberIDByName map[string]int64, warnings *[]string) int64 {
	v, ok := f[column]
	if !ok {
		return local
	}
	name := toText(v)
	if name == "" {
		return 0
	}
	if id, ok := memberIDByName[name]; ok {
		return id
	}
	*warnings = append(*warnings, fmt.Sprintf("%s %q 不在本地成员表，保留原值", column, name))
	return local
}

// unknownFieldWarnings 识别映射集合之外的字段（updated_by 为 pulse 自建、不参与同步的
// 列，静默忽略；其余视为有人在 Bitable 手改表结构，告警忽略）。
func unknownFieldWarnings(f map[string]any, fieldSet map[string]bool) []string {
	var warnings []string
	for k := range f {
		if _, mapped := fieldSet[k]; !mapped && k != "updated_by" {
			warnings = append(warnings, fmt.Sprintf("未知字段 %q 已忽略（可能有人在 Bitable 手改了表结构）", k))
		}
	}
	return warnings
}
