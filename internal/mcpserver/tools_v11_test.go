package mcpserver

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// ---- v1.1 研发交付闭环 15 工具（tools_v11.go）----
// 断言口径（plan Task 4）：
//   - agent 建需求 → list_requirements 可见；
//   - create_bug 关联需求 → list_bugs 状态过滤生效；
//   - update_* 状态流转 → activity actor=agent、on_behalf_of 经 delegated_by；
//   - 全部写工具触发 AutopushFunc（尽力而为，不影响工具结果）。

// TestDeliveryLoopRequirementFlow 需求三工具：create（缺省值/delegated_by 归因）→
// list（可见 + 状态过滤 + 空列表为 []）→ update（状态流转记 update_status）。
func TestDeliveryLoopRequirementFlow(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	created := decode(t, callTool(t, sc, "create_requirement", map[string]any{
		"project": "demo", "title": "需求A", "desc": "背景说明", "delegated_by": "zhang"}))
	if created["Status"] != "proposed" {
		t.Fatalf("create_requirement 缺省 status 须为 proposed: %s", string(mustJSON(created)))
	}
	if created["Priority"] != float64(3) {
		t.Fatalf("create_requirement 缺省 priority 须为 3: %s", string(mustJSON(created)))
	}
	rid := int64(created["ID"].(float64))

	// delegated_by 归因：create 活动执行者是 agent，on_behalf_of 记 zhang
	agent := memberByName(t, s, "claude")
	zhang := memberByName(t, s, "zhang")
	var sawCreate bool
	for _, a := range activities(t, s, 1) {
		if a.EntityType == "requirement" && a.EntityID == rid && a.Action == "create" {
			if a.ActorID != agent.ID || a.OnBehalfOf != zhang.ID {
				t.Fatalf("create 活动 actor=%d behalf=%d, want agent %d on behalf of %d",
					a.ActorID, a.OnBehalfOf, agent.ID, zhang.ID)
			}
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Fatalf("requirement create 活动缺失: %+v", activities(t, s, 1))
	}

	// 列表可见；状态过滤生效
	list := decodeList(t, callTool(t, sc, "list_requirements", map[string]any{"project": "demo"}))
	if len(list) != 1 || list[0].(map[string]any)["Title"] != "需求A" {
		t.Fatalf("list_requirements 必须看到需求A: %s", string(mustJSON(list)))
	}
	if got := decodeList(t, callTool(t, sc, "list_requirements",
		map[string]any{"project": "demo", "status": "accepted"})); len(got) != 0 {
		t.Fatalf("status=accepted 过滤须为空: %s", string(mustJSON(got)))
	}

	// update 状态流转：记 update_status，执行者是 agent
	updated := decode(t, callTool(t, sc, "update_requirement",
		map[string]any{"id": rid, "status": "accepted", "priority": 2.0}))
	if updated["Status"] != "accepted" || updated["Priority"] != float64(2) {
		t.Fatalf("update_requirement 结果不符: %s", string(mustJSON(updated)))
	}
	var sawStatus bool
	for _, a := range activities(t, s, 1) {
		if a.EntityType == "requirement" && a.Action == "update_status" && a.ActorID == agent.ID && a.EntityID == rid {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatalf("update_status 活动缺失或归属错误: %+v", activities(t, s, 1))
	}

	// 错误路径：非法状态（中文提示）、未知 id、未知项目、缺 title
	if msg := callToolErr(t, sc, "update_requirement",
		map[string]any{"id": rid, "status": "doing"}); !strings.Contains(msg, "status 必须为 proposed|reviewing|accepted|in_dev|delivered|rejected") {
		t.Fatalf("非法状态须报中文枚举错误: %s", msg)
	}
	if msg := callToolErr(t, sc, "update_requirement",
		map[string]any{"id": 999, "status": "accepted"}); !strings.Contains(msg, "需求不存在") {
		t.Fatalf("未知需求须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "create_requirement",
		map[string]any{"project": "ghost", "title": "x"}); !strings.Contains(msg, "项目不存在") {
		t.Fatalf("未知项目须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "create_requirement", map[string]any{"project": "demo"}); msg == "" {
		t.Fatal("缺 title 须以 isError 失败")
	}
}

// TestDeliveryLoopBugFlow bug 三工具：关联需求/发现版本/处理人；list 状态与严重级
// 过滤；update 流转 + assignee="me" 归 agent 自己 + 空串清空。
func TestDeliveryLoopBugFlow(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	req := decode(t, callTool(t, sc, "create_requirement", map[string]any{"project": "demo", "title": "需求A"}))
	decode(t, callTool(t, sc, "add_version", map[string]any{"project": "demo", "name": "v1.0"}))

	bug := decode(t, callTool(t, sc, "create_bug", map[string]any{
		"project": "demo", "title": "崩溃", "desc": "打开即崩", "severity": 1.0,
		"assignee": "alice", "requirement_id": req["ID"], "found_version": "v1.0"}))
	if bug["Status"] != "open" {
		t.Fatalf("create_bug 缺省 status 须为 open: %s", string(mustJSON(bug)))
	}
	if bug["RequirementID"] != req["ID"] {
		t.Fatalf("bug 必须关联需求: %s", string(mustJSON(bug)))
	}
	if bug["Severity"] != float64(1) || bug["FoundVersionID"] == float64(0) {
		t.Fatalf("severity/发现版本未落库: %s", string(mustJSON(bug)))
	}
	alice := memberByName(t, s, "alice")
	if bug["AssigneeID"] != float64(alice.ID) {
		t.Fatalf("assignee 须解析为 alice: %s", string(mustJSON(bug)))
	}

	// 缺省 severity=3（P2）
	bug2 := decode(t, callTool(t, sc, "create_bug", map[string]any{"project": "demo", "title": "错别字"}))
	if bug2["Severity"] != float64(3) {
		t.Fatalf("create_bug 缺省 severity 须为 3: %s", string(mustJSON(bug2)))
	}

	// 错误路径：需求不存在 / severity 越界
	if msg := callToolErr(t, sc, "create_bug", map[string]any{
		"project": "demo", "title": "x", "requirement_id": 999}); !strings.Contains(msg, "需求不存在") {
		t.Fatalf("关联不存在需求须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "create_bug", map[string]any{
		"project": "demo", "title": "x", "severity": 9}); !strings.Contains(msg, "severity") {
		t.Fatalf("severity 越界须报错: %s", msg)
	}

	// list 过滤：状态 + 严重级
	if got := decodeList(t, callTool(t, sc, "list_bugs",
		map[string]any{"project": "demo", "status": "open"})); len(got) != 2 {
		t.Fatalf("list_bugs status=open 须为 2 条: %s", string(mustJSON(got)))
	}
	if got := decodeList(t, callTool(t, sc, "list_bugs",
		map[string]any{"project": "demo", "severity": 1.0})); len(got) != 1 {
		t.Fatalf("list_bugs severity=1 须为 1 条: %s", string(mustJSON(got)))
	}

	// update：状态流转 + assignee="me"（agent 自己，不得建名为 me 的成员）
	agent := memberByName(t, s, "claude")
	fixed := decode(t, callTool(t, sc, "update_bug",
		map[string]any{"id": bug["ID"], "status": "fixed", "assignee": "me"}))
	if fixed["Status"] != "fixed" || fixed["AssigneeID"] != float64(agent.ID) {
		t.Fatalf(`update_bug 流转/assignee=me 不符: %s`, string(mustJSON(fixed)))
	}
	if got := decodeList(t, callTool(t, sc, "list_bugs",
		map[string]any{"project": "demo", "status": "open"})); len(got) != 1 {
		t.Fatalf("fixed 后 open 过滤须剩 1 条: %s", string(mustJSON(got)))
	}
	// 空串清空处理人
	cleared := decode(t, callTool(t, sc, "update_bug", map[string]any{"id": bug["ID"], "assignee": ""}))
	if cleared["AssigneeID"] != float64(0) {
		t.Fatalf("空串 assignee 须清空: %s", string(mustJSON(cleared)))
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "me" {
			t.Fatalf(`不得创建名为 "me" 的成员: %+v`, ms)
		}
	}
	if msg := callToolErr(t, sc, "update_bug",
		map[string]any{"id": 999, "status": "fixed"}); !strings.Contains(msg, "bug 不存在") {
		t.Fatalf("未知 bug 须报错: %s", msg)
	}
}

// TestDeliveryLoopCrossProjectRequirementRefused：跨项目需求引用必须按"不存在"
// 拒绝（requireLinkedRequirement 守卫 ProjectID，不泄露他项目内该 id 的存在性）。
func TestDeliveryLoopCrossProjectRequirementRefused(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	requireProject(t, s, "other")
	sc := connect(t, s, "claude")

	req := decode(t, callTool(t, sc, "create_requirement", map[string]any{"project": "demo", "title": "需求A"}))
	msg := callToolErr(t, sc, "create_bug", map[string]any{
		"project": "other", "title": "x", "requirement_id": req["ID"]})
	if !strings.Contains(msg, "需求不存在") {
		t.Fatalf("跨项目需求引用须报 需求不存在: %s", msg)
	}
	if got := decodeList(t, callTool(t, sc, "list_bugs", map[string]any{"project": "other"})); len(got) != 0 {
		t.Fatalf("失败路径不得创建 bug: %s", string(mustJSON(got)))
	}
}

// TestDeliveryLoopSubmissionReleaseFlow 提测/发版四工具：版本必填、状态流转自动
// 补记时间戳、delegated_by 归因到 update_status 活动、draft 不可作为更新目标。
func TestDeliveryLoopSubmissionReleaseFlow(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	callTool(t, sc, "add_version", map[string]any{"project": "demo", "name": "v1.0"})
	req := decode(t, callTool(t, sc, "create_requirement", map[string]any{"project": "demo", "title": "需求A"}))

	sub := decode(t, callTool(t, sc, "create_test_submission", map[string]any{
		"project": "demo", "version": "v1.0", "requirement_id": req["ID"], "test_owner": "alice"}))
	if sub["Status"] != "draft" {
		t.Fatalf("create_test_submission 缺省 status 须为 draft: %s", string(mustJSON(sub)))
	}
	if sub["VersionID"] == float64(0) {
		t.Fatalf("version 须解析落库: %s", string(mustJSON(sub)))
	}
	if sub["SubmittedAt"] != "" {
		t.Fatalf("draft 提测单 submitted_at 须为空: %s", string(mustJSON(sub)))
	}
	alice := memberByName(t, s, "alice")
	if sub["TestOwnerID"] != float64(alice.ID) {
		t.Fatalf("test_owner 须解析为 alice: %s", string(mustJSON(sub)))
	}

	// 错误路径：版本不存在
	if msg := callToolErr(t, sc, "create_test_submission",
		map[string]any{"project": "demo", "version": "v9.9"}); !strings.Contains(msg, "版本不存在") {
		t.Fatalf("未知版本须报错: %s", msg)
	}

	// 状态流转 draft → submitted → passed；时间戳自动补记；delegated_by 归因
	agent := memberByName(t, s, "claude")
	up := decode(t, callTool(t, sc, "update_test_submission",
		map[string]any{"id": sub["ID"], "status": "submitted"}))
	if up["Status"] != "submitted" || up["SubmittedAt"] == "" {
		t.Fatalf("submitted 须补记 submitted_at: %s", string(mustJSON(up)))
	}
	done := decode(t, callTool(t, sc, "update_test_submission",
		map[string]any{"id": sub["ID"], "status": "passed", "delegated_by": "zhang"}))
	if done["ConcludedAt"] == "" {
		t.Fatalf("passed 须补记 concluded_at: %s", string(mustJSON(done)))
	}
	zhang := memberByName(t, s, "zhang")
	var sawStatus bool
	for _, a := range activities(t, s, 1) {
		if a.EntityType == "test_submission" && a.Action == "update_status" &&
			a.ActorID == agent.ID && a.OnBehalfOf == zhang.ID {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatalf("test_submission update_status 活动须归 agent 且 on_behalf_of=zhang: %+v", activities(t, s, 1))
	}

	// draft 仅创建缺省，不可作为更新目标（CLI 同语义）
	if msg := callToolErr(t, sc, "update_test_submission",
		map[string]any{"id": sub["ID"], "status": "draft"}); !strings.Contains(msg, "status 必须为 submitted|testing|passed|failed") {
		t.Fatalf("draft 不可作为更新目标: %s", msg)
	}
	// 未知提测单
	if msg := callToolErr(t, sc, "update_test_submission",
		map[string]any{"id": 999, "status": "testing"}); !strings.Contains(msg, "提测单不存在") {
		t.Fatalf("未知提测单须报错: %s", msg)
	}

	// 按版本过滤
	if got := decodeList(t, callTool(t, sc, "list_test_submissions",
		map[string]any{"project": "demo", "version": "v1.0"})); len(got) != 1 {
		t.Fatalf("list_test_submissions 按版本过滤须 1 条: %s", string(mustJSON(got)))
	}

	// 发版：缺省 preparing → released 补记 released_at
	rel := decode(t, callTool(t, sc, "create_release",
		map[string]any{"project": "demo", "version": "v1.0", "manager": "alice"}))
	if rel["Status"] != "preparing" {
		t.Fatalf("create_release 缺省 status 须为 preparing: %s", string(mustJSON(rel)))
	}
	if msg := callToolErr(t, sc, "update_release",
		map[string]any{"id": rel["ID"], "status": "done"}); !strings.Contains(msg, "status 必须为 preparing|testing|released|rolled_back") {
		t.Fatalf("非法发版状态须报错: %s", msg)
	}
	rls := decode(t, callTool(t, sc, "update_release",
		map[string]any{"id": rel["ID"], "status": "released"}))
	if rls["Status"] != "released" || rls["ReleasedAt"] == "" {
		t.Fatalf("released 须补记 released_at: %s", string(mustJSON(rls)))
	}
	if got := decodeList(t, callTool(t, sc, "list_releases", map[string]any{"project": "demo"})); len(got) != 1 {
		t.Fatalf("list_releases 须 1 条: %s", string(mustJSON(got)))
	}
}

// TestDeliveryLoopReviewAndMeetings 评审与会议：kind 必填且校验、conclusion 缺省
// pending、requirement 过滤；会议 MCP 只读（record 走 CLI），list_meetings 可见。
func TestDeliveryLoopReviewAndMeetings(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	req := decode(t, callTool(t, sc, "create_requirement", map[string]any{"project": "demo", "title": "需求A"}))

	// kind 必填 + 枚举校验 + 关联需求须存在
	if msg := callToolErr(t, sc, "create_review", map[string]any{"project": "demo"}); msg == "" {
		t.Fatal("缺 kind 须以 isError 失败")
	}
	if msg := callToolErr(t, sc, "create_review",
		map[string]any{"project": "demo", "kind": "design"}); !strings.Contains(msg, "kind 必须为 requirement|release|test") {
		t.Fatalf("非法 kind 须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "create_review", map[string]any{
		"project": "demo", "kind": "requirement", "requirement_id": 999}); !strings.Contains(msg, "需求不存在") {
		t.Fatalf("关联不存在需求须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "create_review", map[string]any{
		"project": "demo", "kind": "test", "conclusion": "maybe"}); !strings.Contains(msg, "conclusion 必须为") {
		t.Fatalf("非法 conclusion 须报错: %s", msg)
	}

	rv := decode(t, callTool(t, sc, "create_review", map[string]any{
		"project": "demo", "kind": "requirement", "requirement_id": req["ID"], "delegated_by": "zhang"}))
	if rv["Conclusion"] != "pending" {
		t.Fatalf("create_review 缺省 conclusion 须为 pending: %s", string(mustJSON(rv)))
	}
	if rv["RequirementID"] != req["ID"] {
		t.Fatalf("评审须关联需求: %s", string(mustJSON(rv)))
	}
	if rv["HeldAt"] == "" {
		t.Fatalf("held_at 缺省须为当前时刻: %s", string(mustJSON(rv)))
	}

	// 显式 conclusion 直接落在 create 上
	rv2 := decode(t, callTool(t, sc, "create_review",
		map[string]any{"project": "demo", "kind": "test", "conclusion": "passed"}))
	if rv2["Conclusion"] != "passed" {
		t.Fatalf("create_review conclusion=passed 须直接生效: %s", string(mustJSON(rv2)))
	}

	// list_reviews：全部 + 按需求过滤
	if got := decodeList(t, callTool(t, sc, "list_reviews", map[string]any{"project": "demo"})); len(got) != 2 {
		t.Fatalf("list_reviews 须 2 条: %s", string(mustJSON(got)))
	}
	if got := decodeList(t, callTool(t, sc, "list_reviews",
		map[string]any{"project": "demo", "requirement_id": req["ID"]})); len(got) != 1 {
		t.Fatalf("list_reviews 按需求过滤须 1 条: %s", string(mustJSON(got)))
	}
	if got := decodeList(t, callTool(t, sc, "list_reviews",
		map[string]any{"project": "demo", "requirement_id": 4242})); len(got) != 0 {
		t.Fatalf("list_reviews 无关需求过滤须为空: %s", string(mustJSON(got)))
	}

	// 会议：MCP 只读列表（record 由 CLI/其他工具负责）
	member, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMeeting(model.Meeting{ProjectID: 1, Title: "迭代评审会"}, member, nil); err != nil {
		t.Fatal(err)
	}
	ms := decodeList(t, callTool(t, sc, "list_meetings", map[string]any{"project": "demo"}))
	if len(ms) != 1 || ms[0].(map[string]any)["Title"] != "迭代评审会" {
		t.Fatalf("list_meetings 须看到迭代评审会: %s", string(mustJSON(ms)))
	}
}

// TestConcludeReviewAndCreateMeeting 评审"先记后结"与会议登记的 MCP 工具：
// conclude_review（review_id + conclusion 必填，delegated_by 归因）与 create_meeting
// （project + title 必填，held_at 由 store 落当前时刻），写后 autopush 同其余写工具。
func TestConcludeReviewAndCreateMeeting(t *testing.T) {
	s := testEnv(t)
	p := requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	rv := decode(t, callTool(t, sc, "create_review",
		map[string]any{"project": "demo", "kind": "requirement"}))

	// conclusion 必填 + 枚举校验（conclude 不接受回退 pending）+ 未知评审
	if msg := callToolErr(t, sc, "conclude_review", map[string]any{"review_id": rv["ID"]}); msg == "" {
		t.Fatal("缺 conclusion 须以 isError 失败")
	}
	if msg := callToolErr(t, sc, "conclude_review",
		map[string]any{"review_id": rv["ID"], "conclusion": "pending"}); !strings.Contains(msg, "conclusion 必须为 passed|passed_with_notes|rejected") {
		t.Fatalf("conclude 为 pending 须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "conclude_review",
		map[string]any{"review_id": rv["ID"], "conclusion": "maybe"}); !strings.Contains(msg, "conclusion 必须为 passed|passed_with_notes|rejected") {
		t.Fatalf("非法 conclusion 须报错: %s", msg)
	}
	if msg := callToolErr(t, sc, "conclude_review",
		map[string]any{"review_id": 999, "conclusion": "passed"}); !strings.Contains(msg, "评审不存在") {
		t.Fatalf("未知评审须报错: %s", msg)
	}

	// happy path：下结论 + delegated_by 归因（actor=agent，on_behalf_of=zhang）
	done := decode(t, callTool(t, sc, "conclude_review", map[string]any{
		"review_id": rv["ID"], "conclusion": "passed_with_notes", "delegated_by": "zhang"}))
	if done["Conclusion"] != "passed_with_notes" {
		t.Fatalf("conclude_review 结果不符: %s", string(mustJSON(done)))
	}
	agent := memberByName(t, s, "claude")
	zhang := memberByName(t, s, "zhang")
	var sawConclude bool
	for _, a := range activities(t, s, p.ID) {
		if a.EntityType == "review" && a.Action == "update" && a.EntityID == int64(rv["ID"].(float64)) {
			if a.ActorID != agent.ID || a.OnBehalfOf != zhang.ID {
				t.Fatalf("conclude 活动 actor=%d behalf=%d, want agent %d on behalf of %d",
					a.ActorID, a.OnBehalfOf, agent.ID, zhang.ID)
			}
			if !strings.Contains(a.Detail, `"conclusion"`) {
				t.Fatalf("conclude 活动明细应记 conclusion 字段: %q", a.Detail)
			}
			sawConclude = true
		}
	}
	if !sawConclude {
		t.Fatalf("conclude_review 的 update 活动缺失: %+v", activities(t, s, p.ID))
	}

	// create_meeting：held_at 落当前时刻、created_by 归操作者（agent）、create 活动归因
	mt := decode(t, callTool(t, sc, "create_meeting", map[string]any{
		"project": "demo", "title": "迭代评审会", "delegated_by": "zhang"}))
	if mt["Title"] != "迭代评审会" || mt["HeldAt"] == "" {
		t.Fatalf("create_meeting 结果不符: %s", string(mustJSON(mt)))
	}
	if mt["CreatedBy"] != float64(agent.ID) {
		t.Fatalf("created_by = %v, want 操作者 agent %d", mt["CreatedBy"], agent.ID)
	}
	var sawMeeting bool
	for _, a := range activities(t, s, p.ID) {
		if a.EntityType == "meeting" && a.Action == "create" {
			if a.ActorID != agent.ID || a.OnBehalfOf != zhang.ID {
				t.Fatalf("meeting create 活动 actor=%d behalf=%d, want agent %d on behalf of %d",
					a.ActorID, a.OnBehalfOf, agent.ID, zhang.ID)
			}
			sawMeeting = true
		}
	}
	if !sawMeeting {
		t.Fatalf("meeting create 活动缺失: %+v", activities(t, s, p.ID))
	}
	if got := decodeList(t, callTool(t, sc, "list_meetings", map[string]any{"project": "demo"})); len(got) != 1 {
		t.Fatalf("list_meetings 须看到新会议: %s", string(mustJSON(got)))
	}
	if msg := callToolErr(t, sc, "create_meeting", map[string]any{"project": "demo"}); msg == "" {
		t.Fatal("缺 title 须以 isError 失败（SDK schema 校验或工具校验）")
	}
}

// TestDeliveryLoopEmptyListsStable 空列表输出必须是 []（稳定 JSON），而非 null。
func TestDeliveryLoopEmptyListsStable(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "empty")
	sc := connect(t, s, "claude")

	for _, name := range []string{"list_requirements", "list_bugs", "list_test_submissions", "list_releases", "list_reviews", "list_meetings"} {
		if got := callTool(t, sc, name, map[string]any{"project": "empty"}); got != "[]" {
			t.Fatalf("%s 空列表须输出 []，got %s", name, got)
		}
	}
}

// TestMCPAutopushOnDeliveryLoopWrites 装配 AutopushFunc 后，9 个写工具各触发一次
// 钩子（真装配 + 计数包装，fake 端点同 TestMCPAutopushOnWriteTools）；读工具不触发；
// 工具结果不受同步影响。
func TestMCPAutopushOnDeliveryLoopWrites(t *testing.T) {
	s := testEnv(t)
	cfgPath := config.DefaultPath()
	if err := os.WriteFile(cfgPath, []byte("default_actor: tester\nfeishu:\n  app_id: cli_a1\n  app_secret: sec1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := requireProject(t, s, "demo")
	p.FeishuBitableAppToken = "appB"
	p.FeishuTaskTableID = "tblT"
	p.FeishuVersionTableID = "tblV"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	fakeBitableForAutopush(t)
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}

	sc := connect(t, s, "claude")
	// 钩子装配前建版本（不计数）
	callTool(t, sc, "add_version", map[string]any{"project": "demo", "name": "v1.0"})

	var mu sync.Mutex
	var calls []string
	AutopushFunc = func(st *store.Store, projectKey string) {
		mu.Lock()
		calls = append(calls, projectKey)
		mu.Unlock()
		feishu.BestEffort(st, cfg, projectKey) // 与 cli/mcp.go 相同的真装配
	}
	t.Cleanup(func() { AutopushFunc = nil })

	assertCalls := func(want int, step string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != want {
			t.Fatalf("%s 后钩子应触发 %d 次, got %d (%v)", step, want, len(calls), calls)
		}
		for _, k := range calls {
			if k != "demo" {
				t.Fatalf("钩子 project key 须为 demo, got %q", k)
			}
		}
	}

	req := decode(t, callTool(t, sc, "create_requirement", map[string]any{"project": "demo", "title": "需求A"}))
	assertCalls(1, "create_requirement")
	decode(t, callTool(t, sc, "update_requirement", map[string]any{"id": req["ID"], "status": "accepted"}))
	assertCalls(2, "update_requirement")
	bug := decode(t, callTool(t, sc, "create_bug", map[string]any{"project": "demo", "title": "崩溃", "requirement_id": req["ID"]}))
	assertCalls(3, "create_bug")
	decode(t, callTool(t, sc, "update_bug", map[string]any{"id": bug["ID"], "status": "fixed"}))
	assertCalls(4, "update_bug")
	decode(t, callTool(t, sc, "create_review", map[string]any{"project": "demo", "kind": "requirement", "requirement_id": req["ID"]}))
	assertCalls(5, "create_review")
	sub := decode(t, callTool(t, sc, "create_test_submission", map[string]any{"project": "demo", "version": "v1.0"}))
	assertCalls(6, "create_test_submission")
	decode(t, callTool(t, sc, "update_test_submission", map[string]any{"id": sub["ID"], "status": "submitted"}))
	assertCalls(7, "update_test_submission")
	rel := decode(t, callTool(t, sc, "create_release", map[string]any{"project": "demo", "version": "v1.0"}))
	assertCalls(8, "create_release")
	decode(t, callTool(t, sc, "update_release", map[string]any{"id": rel["ID"], "status": "released"}))
	assertCalls(9, "update_release")

	// 读工具不触发钩子
	callTool(t, sc, "list_requirements", map[string]any{"project": "demo"})
	assertCalls(9, "list_requirements（读不触发）")
}
