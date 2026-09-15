package feishu

// mapping_doclink_test.go：六实体表"协作文档"超链接列的映射 TDD（v1.2）。
//   - push：feishu_doc_token 非空时写 {"text":"打开文档","link":".../docx/<token>"}，
//     计入 hash（token 恒定 → 回声稳定）；空则省略键；
//   - pull：从链接提取 token（取 URL path 最后一段），形态容错（string /
//     {"text","link"} 对象或数组）；"协作文档" 属映射列，不告警未知字段。

import (
	"reflect"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

const wantDocLink = "https://www.feishu.cn/docx/docTOK"

func wantDocField() map[string]any {
	return map[string]any{"text": "打开文档", "link": wantDocLink}
}

// TestCollabDocFieldWrittenWhenTokenPresent：六实体 ToFields 均在 token 非空时写链接列、
// 空时省略键；同一 token 往返后 hash 稳定。
func TestCollabDocFieldWrittenWhenTokenPresent(t *testing.T) {
	idToName := map[int64]string{3: "tester"}
	verIDToName := map[int64]string{5: "v1.0"}
	suites := []struct {
		name   string
		fields map[string]any
	}{
		{"requirement", RequirementToFields(model.Requirement{
			Title: "x", Status: "proposed", Priority: 3, FeishuDocToken: "docTOK",
		}, idToName)},
		{"review", ReviewToFields(model.Review{Kind: "test", Conclusion: "pending",
			FeishuDocToken: "docTOK"}, nil)},
		{"meeting", MeetingToFields(model.Meeting{Title: "x", FeishuDocToken: "docTOK"})},
		{"bug", BugToFields(model.Bug{Title: "x", Severity: 1, Status: "open",
			FeishuDocToken: "docTOK"}, idToName, verIDToName, nil)},
		{"submission", SubmissionToFields(model.TestSubmission{Status: "draft",
			FeishuDocToken: "docTOK"}, idToName, verIDToName, nil)},
		{"release", ReleaseToFields(model.Release{Status: "preparing",
			FeishuDocToken: "docTOK"}, idToName, verIDToName)},
	}
	for _, tc := range suites {
		if !reflect.DeepEqual(tc.fields["协作文档"], wantDocField()) {
			t.Fatalf("%s: 协作文档列应为超链接形态: got %#v want %#v",
				tc.name, tc.fields["协作文档"], wantDocField())
		}
	}
	// 空 token 省略键（六实体）
	empty := []map[string]any{
		RequirementToFields(model.Requirement{Title: "x", Status: "proposed", Priority: 3}, nil),
		ReviewToFields(model.Review{Kind: "test"}, nil),
		MeetingToFields(model.Meeting{Title: "x"}),
		BugToFields(model.Bug{Title: "x", Severity: 1, Status: "open"}, nil, nil, nil),
		SubmissionToFields(model.TestSubmission{Status: "draft"}, nil, nil, nil),
		ReleaseToFields(model.Release{Status: "preparing"}, nil, nil),
	}
	for i, f := range empty {
		if _, ok := f["协作文档"]; ok {
			t.Fatalf("实体[%d]: 空 token 应省略 协作文档 键: %+v", i, f)
		}
	}
}

// TestCollabDocFieldNotUnknown：协作文档 属映射列，远端出现不告警"未知字段"。
func TestCollabDocFieldNotUnknown(t *testing.T) {
	doc := map[string]any{"text": "打开文档", "link": wantDocLink}
	if _, _, w := FieldsToRequirement(map[string]any{
		"需求名": "x", "状态": "proposed", "优先级": "3", "协作文档": doc,
	}, model.Requirement{}, nil); len(w) != 0 {
		t.Fatalf("需求: 协作文档不应告警: %v", w)
	}
	if _, _, w := FieldsToReview(map[string]any{
		"评审类型": "test", "结论": "pending", "协作文档": doc,
	}, model.Review{}, nil); len(w) != 0 {
		t.Fatalf("评审: 协作文档不应告警: %v", w)
	}
	if _, _, w := FieldsToMeeting(map[string]any{
		"会议标题": "x", "协作文档": doc,
	}, model.Meeting{}); len(w) != 0 {
		t.Fatalf("会议: 协作文档不应告警: %v", w)
	}
	if _, _, w := FieldsToBug(map[string]any{
		"标题": "x", "严重级": "1", "状态": "open", "协作文档": doc,
	}, model.Bug{}, nil, nil, nil); len(w) != 0 {
		t.Fatalf("bug: 协作文档不应告警: %v", w)
	}
	if _, _, w := FieldsToSubmission(map[string]any{
		"版本": "v1.0", "状态": "draft", "协作文档": doc,
	}, model.TestSubmission{}, nil, map[string]int64{"v1.0": 5}, nil); len(w) != 0 {
		t.Fatalf("提测: 协作文档不应告警: %v", w)
	}
	if _, _, w := FieldsToRelease(map[string]any{
		"版本": "v1.0", "状态": "preparing", "协作文档": doc,
	}, model.Release{}, nil, map[string]int64{"v1.0": 5}); len(w) != 0 {
		t.Fatalf("发版: 协作文档不应告警: %v", w)
	}
}

// TestCollabDocTokenExtraction：从链接提取 token（URL path 最后一段），形态容错。
func TestCollabDocTokenExtraction(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"裸链接字符串", wantDocLink, "docTOK"},
		{"对象形态", map[string]any{"text": "打开文档", "link": wantDocLink}, "docTOK"},
		{"对象数组形态", []any{map[string]any{"text": "打开文档", "link": wantDocLink}}, "docTOK"},
		{"text 兜底", map[string]any{"text": wantDocLink}, "docTOK"},
		{"带查询参数", wantDocLink + "?from=x#frag", "docTOK"},
		{"直接存 token", "docTOK", "docTOK"},
		{"空串", "", ""},
		{"垃圾对象", map[string]any{"foo": "bar"}, ""},
		{"垃圾数组", []any{"不是字典"}, ""},
		{"nil", nil, ""},
		{"数字", float64(3), ""},
	}
	for _, tc := range cases {
		if got := collabDocToken(tc.in); got != tc.want {
			t.Fatalf("%s: collabDocToken(%#v) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
