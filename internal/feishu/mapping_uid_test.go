package feishu

// mapping_uid_test.go：需求全局身份（UID）映射的 TDD（v1.2）。
//   - 需求表新增 需求UID 列（全局身份，随记录同步）；
//   - 评审/bug/提测 的 需求ID 列内容由本地 id 字符串改为需求 UID，pull 侧解析回本地 id；
//   - 旧 base 的数字串（旧本地 id）容忍解析一次并告警提示迁移；
//   - 查不到的引用保留本地关联并告警（对齐"版本不在本地"风格）。

import (
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// TestRequirementUIDFieldRoundTrip：需求UID 参与映射与往返（hash/回声根基）；富文本形态安全。
func TestRequirementUIDFieldRoundTrip(t *testing.T) {
	r := model.Requirement{Title: "导出报表", Status: "in_dev", Priority: 2, UID: "aabbccdd00112233aabbccdd00112233"}
	fields := RequirementToFields(r, nil)
	if fields["需求UID"] != r.UID {
		t.Fatalf("需求UID 应参与映射: %+v", fields)
	}
	got, missing, warns := FieldsToRequirement(fields, model.Requirement{}, nil)
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	if got.UID != r.UID {
		t.Fatalf("需求UID 往返不一致: %q vs %q", got.UID, r.UID)
	}
	if ContentHash(fields) != ContentHash(RequirementToFields(got, nil)) {
		t.Fatal("往返后 hash 必须一致（自回声判定的根基）")
	}
	// 空 UID 省略键（不向旧列写空串）
	if _, ok := RequirementToFields(model.Requirement{Title: "x", Status: "proposed", Priority: 3}, nil)["需求UID"]; ok {
		t.Fatal("空 UID 应省略 需求UID 键")
	}
	// 富文本形态读回安全（真实租户 text 列形态）
	changed, _, _ := FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "proposed", "优先级": "3",
		"需求UID": []any{map[string]any{"type": "text", "text": "ffeeddcc00112233ffeeddcc00112233"}},
	}, model.Requirement{}, nil)
	if changed.UID != "ffeeddcc00112233ffeeddcc00112233" {
		t.Fatalf("富文本形态的 需求UID 应展平读取: %q", changed.UID)
	}
	// 旧 base 无该列（键缺失）：保留本地 UID，不被清空
	kept, _, _ := FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "proposed", "优先级": "3",
	}, model.Requirement{UID: "aabbccdd00112233aabbccdd00112233"}, nil)
	if kept.UID != "aabbccdd00112233aabbccdd00112233" {
		t.Fatalf("键缺失应保留本地 UID: %q", kept.UID)
	}
}

// TestReviewRequirementRefByUID：评审 需求ID 列写 UID、读回解析；未知 UID 保留本地并告警；
// 旧本地 id 数字串容忍解析并告警提示迁移；创建即定语义不变。
func TestReviewRequirementRefByUID(t *testing.T) {
	uidX := "aabbccdd00112233aabbccdd00112233"
	idToUID := map[int64]string{7: uidX}
	uidToID := map[string]int64{uidX: 7}

	// 写：需求ID 列内容为 UID
	v := model.Review{Kind: "requirement", Conclusion: "pending", RequirementID: 7}
	if got := ReviewToFields(v, idToUID)["需求ID"]; got != uidX {
		t.Fatalf("评审 需求ID 应写 UID: got %q want %q", got, uidX)
	}
	// 无关联：空串
	if got := ReviewToFields(model.Review{Kind: "test"}, idToUID)["需求ID"]; got != "" {
		t.Fatalf("无关联需求ID 应为空串: %q", got)
	}
	// 读：UID 解析回本地 id
	changed, _, warns := FieldsToReview(map[string]any{
		"评审类型": "requirement", "结论": "pending", "需求ID": uidX,
	}, model.Review{}, uidToID)
	if changed.RequirementID != 7 || len(warns) != 0 {
		t.Fatalf("需求UID 应解析回本地 id: %+v %v", changed, warns)
	}
	// 读：未知 UID（跨机尚未拉到需求）→ 保留本地关联并告警
	changed, _, warns = FieldsToReview(map[string]any{
		"评审类型": "requirement", "结论": "pending", "需求ID": "ffffffffffffffffffffffffffffffff",
	}, model.Review{RequirementID: 3}, nil)
	if changed.RequirementID != 3 || len(warns) != 1 || !strings.Contains(warns[0], "ffffffff") {
		t.Fatalf("未知 UID 应保留本地关联并告警: %+v %v", changed, warns)
	}
	// 读：旧本地 id 数字串（legacy）→ 容忍解析一次并告警提示迁移
	changed, _, warns = FieldsToReview(map[string]any{
		"评审类型": "requirement", "结论": "pending", "需求ID": "7",
	}, model.Review{}, uidToID)
	if changed.RequirementID != 7 || len(warns) != 1 || !strings.Contains(warns[0], "旧本地 id") {
		t.Fatalf("旧数字串应容忍解析并告警迁移: %+v %v", changed, warns)
	}
	// 创建即定：base 已有值时远端不一致仅告警、保留本地
	changed, _, warns = FieldsToReview(map[string]any{
		"评审类型": "requirement", "结论": "rejected", "需求ID": uidX,
	}, model.Review{RequirementID: 2}, uidToID)
	if changed.RequirementID != 2 || len(warns) == 0 {
		t.Fatalf("需求ID 创建即定语义应保留: %+v %v", changed, warns)
	}
}

// TestBugSubmissionRequirementRefByUID：bug/提测表新增 需求ID 列（UID 形态）的读写语义。
func TestBugSubmissionRequirementRefByUID(t *testing.T) {
	uidX := "11223344556677881122334455667788"
	idToUID := map[int64]string{5: uidX}
	uidToID := map[string]int64{uidX: 5}

	// bug 写：需求ID 列为 UID；无关联为空串
	bf := BugToFields(model.Bug{Title: "b", Severity: 1, Status: "open", RequirementID: 5}, nil, nil, idToUID)
	if bf["需求ID"] != uidX {
		t.Fatalf("bug 需求ID 应写 UID: %+v", bf)
	}
	if got := BugToFields(model.Bug{Title: "b", Severity: 1, Status: "open"}, nil, nil, idToUID)["需求ID"]; got != "" {
		t.Fatalf("bug 无关联需求ID 应为空串: %q", got)
	}
	// bug 读：UID 解析（bug 的需求引用可随远端更新，UpdateBug 有通道）
	bg, _, warns := FieldsToBug(map[string]any{
		"标题": "b", "严重级": "1", "状态": "open", "需求ID": uidX,
	}, model.Bug{}, nil, nil, uidToID)
	if bg.RequirementID != 5 || len(warns) != 0 {
		t.Fatalf("bug 需求UID 应解析回本地 id: %+v %v", bg, warns)
	}
	// bug 读：未知 UID 保留本地并告警
	bg, _, warns = FieldsToBug(map[string]any{
		"标题": "b", "严重级": "1", "状态": "open", "需求ID": "ffffffffffffffffffffffffffffffff",
	}, model.Bug{RequirementID: 2}, nil, nil, nil)
	if bg.RequirementID != 2 || len(warns) != 1 {
		t.Fatalf("bug 未知 UID 应保留本地关联并告警: %+v %v", bg, warns)
	}

	// 提测写：需求ID 列为 UID
	sf := SubmissionToFields(model.TestSubmission{Status: "draft", RequirementID: 5}, nil, nil, idToUID)
	if sf["需求ID"] != uidX {
		t.Fatalf("提测 需求ID 应写 UID: %+v", sf)
	}
	// 提测读：UID 解析；创建即定（store 无更新通道）——base 已有值保留本地
	sg, _, _ := FieldsToSubmission(map[string]any{
		"版本": "v1.0", "状态": "draft", "需求ID": uidX,
	}, model.TestSubmission{}, nil, nil, uidToID)
	if sg.RequirementID != 5 {
		t.Fatalf("提测 需求UID 应解析回本地 id: %+v", sg)
	}
	sg, _, warns = FieldsToSubmission(map[string]any{
		"版本": "v1.0", "状态": "draft", "需求ID": uidX,
	}, model.TestSubmission{RequirementID: 2}, nil, nil, uidToID)
	if sg.RequirementID != 2 || len(warns) == 0 {
		t.Fatalf("提测 需求ID 创建即定应保留本地: %+v %v", sg, warns)
	}
}
