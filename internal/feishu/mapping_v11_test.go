package feishu

// mapping_v11_test.go：六实体 Bitable 字段映射的 TDD。
// 核心性质与任务表一致——"往返一致"（XToFields ∘ FieldsToX = id）是自回声判定的根基；
// 另覆盖：必填字段缺失跳过、未知字段/updated_by 的告警语义、成员与版本按名解析、
// 评审与会议的"创建即定"字段语义。

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func TestRequirementFieldsRoundTrip(t *testing.T) {
	nameToID := map[string]int64{"tester": 3}
	r := model.Requirement{Title: "导出报表", Description: "支持 CSV", Status: "in_dev",
		Priority: 2, OwnerID: 3}
	fields := RequirementToFields(r, map[int64]string{3: "tester"})
	if fields["需求名"] != "导出报表" || fields["状态"] != "in_dev" || fields["负责人"] != "tester" ||
		fields["优先级"] != "2" || fields["描述"] != "支持 CSV" || fields["已废弃"] != false {
		t.Fatalf("需求字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToRequirement(fields, model.Requirement{}, nameToID)
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	fields2 := RequirementToFields(got, map[int64]string{3: "tester"})
	if !reflect.DeepEqual(fields, fields2) {
		t.Fatalf("往返不一致:\n got  %+v\n want %+v", fields2, fields)
	}
	if ContentHash(fields) != ContentHash(fields2) {
		t.Fatal("往返后 hash 必须一致（自回声判定的根基）")
	}
}

func TestRequirementFieldsResolution(t *testing.T) {
	// 负责人不在本地成员表：保留原负责人并告警
	local := model.Requirement{Title: "x", Status: "proposed", Priority: 3, OwnerID: 9}
	changed, missing, warns := FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "reviewing", "优先级": "1", "负责人": "陌生人",
	}, local, map[string]int64{"tester": 3})
	if len(missing) != 0 || len(warns) != 1 || changed.OwnerID != 9 {
		t.Fatalf("未知负责人应保留并告警: %+v %v %v", changed, missing, warns)
	}
	// 负责人为空：清空
	changed, _, _ = FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "reviewing", "优先级": "1", "负责人": "",
	}, local, map[string]int64{})
	if changed.OwnerID != 0 {
		t.Fatalf("空负责人应清空: %+v", changed)
	}
	// 缺必填字段：整条跳过
	_, missing, _ = FieldsToRequirement(map[string]any{"需求名": "x", "状态": "reviewing"},
		model.Requirement{}, nil)
	if len(missing) != 1 || missing[0] != "优先级" {
		t.Fatalf("缺优先级应报 missing: %v", missing)
	}
	// 优先级无法解析：保留原值并告警
	changed, _, warns = FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "reviewing", "优先级": "高", "描述": "d",
	}, local, nil)
	if changed.Priority != 3 || len(warns) != 1 {
		t.Fatalf("无法解析的优先级应保留并告警: %+v %v", changed, warns)
	}
}

func TestReviewFieldsRoundTripAndImmutableSemantics(t *testing.T) {
	v := model.Review{Kind: "requirement", HeldAt: "2026-09-15 10:00:00",
		Conclusion: "passed_with_notes", RequirementID: 7}
	fields := ReviewToFields(v)
	if fields["评审类型"] != "requirement" || fields["结论"] != "passed_with_notes" ||
		fields["评审时间"] != "2026-09-15 10:00:00" || fields["需求ID"] != "7" {
		t.Fatalf("评审字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToReview(fields, model.Review{})
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	fields2 := ReviewToFields(got)
	if !reflect.DeepEqual(fields, fields2) {
		t.Fatalf("往返不一致:\n got  %+v\n want %+v", fields2, fields)
	}
	// 无关联需求：需求ID 落空串
	if ReviewToFields(model.Review{Kind: "test", Conclusion: "pending"})["需求ID"] != "" {
		t.Fatal("无关联需求的需求ID 应为空串")
	}
	// 创建即定：base 已有 kind/held_at/需求ID 时保留本地值，远端不一致仅告警
	base := model.Review{Kind: "release", HeldAt: "09-01", Conclusion: "pending", RequirementID: 2}
	changed, _, warns := FieldsToReview(map[string]any{
		"评审类型": "requirement", "结论": "rejected", "评审时间": "09-02", "需求ID": "5",
	}, base)
	if changed.Kind != "release" || changed.HeldAt != "09-01" || changed.RequirementID != 2 {
		t.Fatalf("创建即定字段应保留本地值: %+v", changed)
	}
	if changed.Conclusion != "rejected" || len(warns) == 0 {
		t.Fatalf("结论应合入且不一致字段应告警: %+v %v", changed, warns)
	}
	// 缺必填字段
	_, missing, _ = FieldsToReview(map[string]any{"评审类型": "test"}, model.Review{})
	if len(missing) != 1 || missing[0] != "结论" {
		t.Fatalf("缺结论应报 missing: %v", missing)
	}
}

func TestMeetingFieldsRoundTrip(t *testing.T) {
	m := model.Meeting{Title: "迭代评审会", HeldAt: "2026-09-15 09:00:00"}
	fields := MeetingToFields(m)
	if fields["会议标题"] != "迭代评审会" || fields["时间"] != "2026-09-15 09:00:00" || fields["已废弃"] != false {
		t.Fatalf("会议字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToMeeting(fields, model.Meeting{})
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	if !reflect.DeepEqual(fields, MeetingToFields(got)) {
		t.Fatalf("往返不一致: %+v vs %+v", MeetingToFields(got), fields)
	}
	// 创建即定：已有标题/时间保留本地
	changed, _, warns := FieldsToMeeting(map[string]any{"会议标题": "改题", "时间": "x"}, m)
	if changed.Title != "迭代评审会" || changed.HeldAt != "2026-09-15 09:00:00" || len(warns) == 0 {
		t.Fatalf("会议标题/时间应创建即定: %+v %v", changed, warns)
	}
	// 缺标题跳过
	_, missing, _ = FieldsToMeeting(map[string]any{}, model.Meeting{})
	if len(missing) != 1 {
		t.Fatalf("缺会议标题应报 missing: %v", missing)
	}
}

func TestBugFieldsRoundTripAndResolution(t *testing.T) {
	nameToID := map[string]int64{"tester": 3}
	verNameToID := map[string]int64{"v1.0": 5}
	b := model.Bug{Title: "登录崩溃", Severity: 1, Status: "fixing", AssigneeID: 3, FoundVersionID: 5}
	fields := BugToFields(b, map[int64]string{3: "tester"}, map[int64]string{5: "v1.0"})
	if fields["标题"] != "登录崩溃" || fields["严重级"] != "1" || fields["状态"] != "fixing" ||
		fields["负责人"] != "tester" || fields["发现版本"] != "v1.0" {
		t.Fatalf("bug 字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToBug(fields, model.Bug{}, nameToID, verNameToID)
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	if !reflect.DeepEqual(fields, BugToFields(got, map[int64]string{3: "tester"}, map[int64]string{5: "v1.0"})) {
		t.Fatalf("往返不一致: %+v", BugToFields(got, map[int64]string{3: "tester"}, map[int64]string{5: "v1.0"}))
	}
	// 严重级非法：保留原值并告警
	changed, _, warns := FieldsToBug(map[string]any{
		"标题": "x", "严重级": "9", "状态": "open",
	}, model.Bug{Severity: 3}, nil, nil)
	if changed.Severity != 3 || len(warns) != 1 {
		t.Fatalf("非法严重级应保留并告警: %+v %v", changed, warns)
	}
	// 发现版本不在本地：保留原版本并告警
	changed, _, warns = FieldsToBug(map[string]any{
		"标题": "x", "严重级": "2", "状态": "open", "发现版本": "v9",
	}, model.Bug{FoundVersionID: 5}, nil, map[string]int64{"v1.0": 5})
	if changed.FoundVersionID != 5 || len(warns) != 1 {
		t.Fatalf("未知版本应保留并告警: %+v %v", changed, warns)
	}
	// 缺必填
	_, missing, _ = FieldsToBug(map[string]any{"标题": "x", "严重级": "2"}, model.Bug{}, nil, nil)
	if len(missing) != 1 || missing[0] != "状态" {
		t.Fatalf("缺状态应报 missing: %v", missing)
	}
}

func TestSubmissionFieldsRoundTripAndResolution(t *testing.T) {
	idToName := map[int64]string{3: "tester", 4: "qa"}
	verIDToName := map[int64]string{5: "v1.0"}
	nameToID := map[string]int64{"tester": 3, "qa": 4}
	verNameToID := map[string]int64{"v1.0": 5}
	sub := model.TestSubmission{VersionID: 5, Status: "submitted", SubmittedBy: 3,
		TestOwnerID: 4, Scope: "核心路径"}
	fields := SubmissionToFields(sub, idToName, verIDToName)
	if fields["版本"] != "v1.0" || fields["状态"] != "submitted" || fields["提测人"] != "tester" ||
		fields["测试负责人"] != "qa" || fields["范围"] != "核心路径" {
		t.Fatalf("提测字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToSubmission(fields, model.TestSubmission{}, nameToID, verNameToID)
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	if !reflect.DeepEqual(fields, SubmissionToFields(got, idToName, verIDToName)) {
		t.Fatalf("往返不一致: %+v", SubmissionToFields(got, idToName, verIDToName))
	}
	// 测试负责人为空（键存在但值为空）→ 0；键缺失则保留本地（Bitable 清列形态）
	changed, _, _ := FieldsToSubmission(map[string]any{
		"版本": "v1.0", "状态": "testing", "提测人": "tester", "测试负责人": "",
	}, model.TestSubmission{TestOwnerID: 4}, nameToID, verNameToID)
	if changed.TestOwnerID != 0 || changed.Status != "testing" {
		t.Fatalf("空测试负责人应清空: %+v", changed)
	}
	changed, _, _ = FieldsToSubmission(map[string]any{
		"版本": "v1.0", "状态": "testing", "提测人": "tester",
	}, model.TestSubmission{TestOwnerID: 4}, nameToID, verNameToID)
	if changed.TestOwnerID != 4 {
		t.Fatalf("键缺失应保留本地测试负责人: %+v", changed)
	}
	// 缺必填
	_, missing, _ = FieldsToSubmission(map[string]any{"状态": "draft"}, model.TestSubmission{}, nil, nil)
	if len(missing) != 1 || missing[0] != "版本" {
		t.Fatalf("缺版本应报 missing: %v", missing)
	}
}

func TestReleaseFieldsRoundTripAndResolution(t *testing.T) {
	idToName := map[int64]string{3: "tester"}
	verIDToName := map[int64]string{5: "v1.0"}
	nameToID := map[string]int64{"tester": 3}
	verNameToID := map[string]int64{"v1.0": 5}
	rel := model.Release{VersionID: 5, Status: "released", ReleaseManagerID: 3,
		ReleasedAt: "2026-09-15 12:00:00", Notes: "灰度 10%"}
	fields := ReleaseToFields(rel, idToName, verIDToName)
	if fields["版本"] != "v1.0" || fields["状态"] != "released" || fields["发布负责人"] != "tester" ||
		fields["发布时间"] != "2026-09-15 12:00:00" || fields["备注"] != "灰度 10%" {
		t.Fatalf("发版字段不符: %+v", fields)
	}
	got, missing, warns := FieldsToRelease(fields, model.Release{}, nameToID, verNameToID)
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	if !reflect.DeepEqual(fields, ReleaseToFields(got, idToName, verIDToName)) {
		t.Fatalf("往返不一致: %+v", ReleaseToFields(got, idToName, verIDToName))
	}
	// 发布时间为创建即定：base 已有则保留
	changed, _, warns := FieldsToRelease(map[string]any{
		"版本": "v1.0", "状态": "released", "发布时间": "其他时间",
	}, model.Release{ReleasedAt: "本地时间"}, nameToID, verNameToID)
	if changed.ReleasedAt != "本地时间" || len(warns) == 0 {
		t.Fatalf("发布时间应创建即定: %+v %v", changed, warns)
	}
	// 缺必填
	_, missing, _ = FieldsToRelease(map[string]any{"版本": "v1.0"}, model.Release{}, nil, nil)
	if len(missing) != 1 || missing[0] != "状态" {
		t.Fatalf("缺状态应报 missing: %v", missing)
	}
}

// TestEntityFieldsIgnoreUpdatedBy：updated_by 列 pulse 自己建但不参与同步（与任务表约定一致），
// 远端出现时静默忽略、不告警；其余未知字段告警。
func TestEntityFieldsIgnoreUpdatedBy(t *testing.T) {
	_, _, reqWarns := FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "proposed", "优先级": "3", "updated_by": true, "神秘列": 1,
	}, model.Requirement{}, nil)
	if len(reqWarns) != 1 || !strings.Contains(reqWarns[0], "神秘列") {
		t.Fatalf("updated_by 应静默忽略、未知字段应告警: %v", reqWarns)
	}
	suites := []struct {
		name  string
		warns []string
	}{
		{"review", reviewWarns()}, {"meeting", meetingWarns()}, {"bug", bugWarns()},
		{"submission", submissionWarns()}, {"release", releaseWarns()},
	}
	for _, tc := range suites {
		for _, w := range tc.warns {
			if strings.Contains(w, "updated_by") {
				t.Fatalf("%s: updated_by 不应告警: %v", tc.name, tc.warns)
			}
		}
	}
}

func reviewWarns() []string {
	_, _, w := FieldsToReview(map[string]any{"评审类型": "test", "结论": "pending", "updated_by": true}, model.Review{})
	return w
}
func meetingWarns() []string {
	_, _, w := FieldsToMeeting(map[string]any{"会议标题": "x", "updated_by": true}, model.Meeting{})
	return w
}
func bugWarns() []string {
	_, _, w := FieldsToBug(map[string]any{"标题": "x", "严重级": "1", "状态": "open", "updated_by": true}, model.Bug{}, nil, nil)
	return w
}
func submissionWarns() []string {
	_, _, w := FieldsToSubmission(map[string]any{"版本": "v", "状态": "draft", "updated_by": true}, model.TestSubmission{}, nil, nil)
	return w
}
func releaseWarns() []string {
	_, _, w := FieldsToRelease(map[string]any{"版本": "v", "状态": "preparing", "updated_by": true}, model.Release{}, nil, nil)
	return w
}
