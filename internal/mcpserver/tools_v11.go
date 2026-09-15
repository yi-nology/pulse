// tools_v11.go 注册 v1.1 研发交付闭环的 15 个 MCP 工具（spec §3.1-3.2，plan Task 4）：
//
//	requirement: create_requirement / update_requirement / list_requirements
//	bug:         create_bug / update_bug / list_bugs
//	test_submissions: create_test_submission / update_test_submission / list_test_submissions
//	release:     create_release / update_release / list_releases
//	review:      create_review / list_reviews
//	meeting:     list_meetings（会议登记走 CLI/记录文档，MCP 只读）
//
// 沿用 server.go 的全部约定：requiredDesc 句式、jsonText 两空格缩进 JSON、
// actor.resolve 的 delegated_by 归因（写工具全部支持）、写后 AutopushFunc 尽力同步。
// 参数语义与 CLI（internal/cli/{requirement,bug,review,submission,release}.go）对齐：
// 状态枚举在工具层先做中文校验，引用（需求/版本/成员）不存在时报中文错误。
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// ---- 状态/枚举校验（与 store 的 CHECK 约束及 CLI 提示文案逐字一致）----

func checkRequirementStatus(v string) error {
	for _, s := range []string{"proposed", "reviewing", "accepted", "in_dev", "delivered", "rejected"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 proposed|reviewing|accepted|in_dev|delivered|rejected，收到 %q", v)
}

func checkBugStatus(v string) error {
	for _, s := range []string{"open", "fixing", "fixed", "verified", "closed", "wontfix"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 open|fixing|fixed|verified|closed|wontfix，收到 %q", v)
}

func checkBugSeverity(v int64) error {
	if v < 1 || v > 4 {
		return fmt.Errorf("severity 必须为 1..4（P0-P3），收到 %d", v)
	}
	return nil
}

func checkReviewKind(v string) error {
	for _, s := range []string{"requirement", "release", "test"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("kind 必须为 requirement|release|test，收到 %q", v)
}

func checkReviewConclusion(v string) error {
	for _, s := range []string{"pending", "passed", "passed_with_notes", "rejected"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("conclusion 必须为 pending|passed|passed_with_notes|rejected，收到 %q", v)
}

// checkSubmissionUpdateStatus draft 仅创建缺省，不作为更新目标（CLI submit update 同语义）。
func checkSubmissionUpdateStatus(v string) error {
	for _, s := range []string{"submitted", "testing", "passed", "failed"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 submitted|testing|passed|failed，收到 %q", v)
}

func checkReleaseStatus(v string) error {
	for _, s := range []string{"preparing", "testing", "released", "rolled_back"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 preparing|testing|released|rolled_back，收到 %q", v)
}

// requireLinkedRequirement 关联需求引用校验（CLI 同文案）：须已存在。
func (c *core) requireLinkedRequirement(id int64) error {
	if _, found, err := c.st.GetRequirement(id); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("需求不存在: id=%d", id)
	}
	return nil
}

// RegisterDeliveryLoopTools 把 v1.1 交付闭环 15 工具注册到 srv；由 Register 在核心
// 工具之后调用，依赖经 core 注入（与 server.go 的工具同一结构体，无新依赖形态）。
func RegisterDeliveryLoopTools(srv *mcp.Server, c *core) {
	mcp.AddTool(srv, &mcp.Tool{Name: "create_requirement",
		Description: "创建需求（status 缺省 proposed，priority 缺省 3）。" + requiredDesc}, c.createRequirement)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_requirement",
		Description: "更新需求状态/负责人/优先级（状态变更记 update_status）。" + requiredDesc}, c.updateRequirement)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_requirements",
		Description: "按项目列出需求，支持 status 过滤。" + requiredDesc}, c.listRequirements)

	mcp.AddTool(srv, &mcp.Tool{Name: "create_bug",
		Description: "创建 bug（severity 缺省 3=P2，status 缺省 open，可关联需求与发现版本）。" + requiredDesc}, c.createBug)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_bug",
		Description: "更新 bug 状态/严重级/处理人（无 reopen 语义）。" + requiredDesc}, c.updateBug)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_bugs",
		Description: "按项目列出 bug，支持 status/severity 过滤。" + requiredDesc}, c.listBugs)

	mcp.AddTool(srv, &mcp.Tool{Name: "create_test_submission",
		Description: "创建提测单（版本必填，status 缺省 draft，提测人归操作者）。" + requiredDesc}, c.createTestSubmission)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_test_submission",
		Description: "流转提测单状态（提交/结论时间由 store 自动补记）。" + requiredDesc}, c.updateTestSubmission)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_test_submissions",
		Description: "按项目列出提测单，支持 version 过滤。" + requiredDesc}, c.listTestSubmissions)

	mcp.AddTool(srv, &mcp.Tool{Name: "create_release",
		Description: "登记发版记录（版本必填，status 缺省 preparing）。" + requiredDesc}, c.createRelease)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_release",
		Description: "流转发版状态（进入 released 自动补记发布时间）。" + requiredDesc}, c.updateRelease)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_releases",
		Description: "按项目列出发版记录。" + requiredDesc}, c.listReleases)

	mcp.AddTool(srv, &mcp.Tool{Name: "create_review",
		Description: "记录一次评审（kind 必填，conclusion 缺省 pending）。" + requiredDesc}, c.createReview)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_reviews",
		Description: "按项目列出评审记录，支持按关联需求过滤。" + requiredDesc}, c.listReviews)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_meetings",
		Description: "按项目列出会议记录。" + requiredDesc}, c.listMeetings)
}

// ---- 工具入参 ----

type createRequirementIn struct {
	Project     string  `json:"project" jsonschema:"项目 key（必填）"`
	Title       string  `json:"title" jsonschema:"需求标题（必填）"`
	Desc        string  `json:"desc,omitempty" jsonschema:"需求描述/背景"`
	Status      string  `json:"status,omitempty" jsonschema:"proposed|reviewing|accepted|in_dev|delivered|rejected，缺省 proposed"`
	Priority    *int64  `json:"priority,omitempty" jsonschema:"优先级，数字越小越优先，缺省 3"`
	Owner       *string `json:"owner,omitempty" jsonschema:"需求负责人成员名；me 表示当前 agent 自己"`
	DelegatedBy string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名（activity 记 on_behalf_of）"`
}

// updateRequirementIn 与 CLI requirement update 同字段（status/owner/priority）；
// owner 空串清空。
type updateRequirementIn struct {
	ID          int64   `json:"id" jsonschema:"需求 ID（必填，先 list_requirements 确认）"`
	Status      *string `json:"status,omitempty" jsonschema:"proposed|reviewing|accepted|in_dev|delivered|rejected"`
	Priority    *int64  `json:"priority,omitempty" jsonschema:"优先级，数字越小越优先"`
	Owner       *string `json:"owner,omitempty" jsonschema:"需求负责人成员名；me 表示当前 agent 自己；空串清空"`
	DelegatedBy string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listRequirementsIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
	Status  string `json:"status,omitempty" jsonschema:"proposed|reviewing|accepted|in_dev|delivered|rejected"`
}

type createBugIn struct {
	Project       string  `json:"project" jsonschema:"项目 key（必填）"`
	Title         string  `json:"title" jsonschema:"bug 标题（必填）"`
	Desc          string  `json:"desc,omitempty" jsonschema:"bug 描述/复现步骤"`
	Severity      *int64  `json:"severity,omitempty" jsonschema:"严重级 1-4（P0-P3），缺省 3=P2"`
	Assignee      *string `json:"assignee,omitempty" jsonschema:"处理人成员名；me 表示当前 agent 自己"`
	RequirementID *int64  `json:"requirement_id,omitempty" jsonschema:"关联需求 ID（须已存在）"`
	FoundVersion  string  `json:"found_version,omitempty" jsonschema:"发现版本：版本 ID 整数，或项目内版本名"`
	DelegatedBy   string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

// updateBugIn 与 CLI bug update 同字段（status/assignee/severity）；assignee 空串清空。
type updateBugIn struct {
	ID          int64   `json:"id" jsonschema:"bug ID（必填，先 list_bugs 确认）"`
	Status      *string `json:"status,omitempty" jsonschema:"open|fixing|fixed|verified|closed|wontfix"`
	Severity    *int64  `json:"severity,omitempty" jsonschema:"严重级 1-4（P0-P3）"`
	Assignee    *string `json:"assignee,omitempty" jsonschema:"处理人成员名；me 表示当前 agent 自己；空串清空"`
	DelegatedBy string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listBugsIn struct {
	Project  string `json:"project" jsonschema:"项目 key（必填）"`
	Status   string `json:"status,omitempty" jsonschema:"open|fixing|fixed|verified|closed|wontfix"`
	Severity *int64 `json:"severity,omitempty" jsonschema:"按严重级过滤：1-4（P0-P3）"`
}

type createTestSubmissionIn struct {
	Project       string  `json:"project" jsonschema:"项目 key（必填）"`
	Version       string  `json:"version" jsonschema:"提测版本：版本 ID 整数，或项目内版本名（必填，需已创建）"`
	RequirementID *int64  `json:"requirement_id,omitempty" jsonschema:"关联需求 ID（须已存在）"`
	TestOwner     *string `json:"test_owner,omitempty" jsonschema:"测试负责人成员名；me 表示当前 agent 自己"`
	DelegatedBy   string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type updateTestSubmissionIn struct {
	ID          int64  `json:"id" jsonschema:"提测单 ID（必填，先 list_test_submissions 确认）"`
	Status      string `json:"status" jsonschema:"目标状态：submitted|testing|passed|failed（必填）"`
	DelegatedBy string `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listTestSubmissionsIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
	Version string `json:"version,omitempty" jsonschema:"按版本过滤：版本 ID 整数，或项目内版本名"`
}

type createReleaseIn struct {
	Project     string  `json:"project" jsonschema:"项目 key（必填）"`
	Version     string  `json:"version" jsonschema:"发版对应版本：版本 ID 整数，或项目内版本名（必填，需已创建）"`
	Manager     *string `json:"manager,omitempty" jsonschema:"发布负责人成员名；me 表示当前 agent 自己"`
	DelegatedBy string  `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type updateReleaseIn struct {
	ID          int64  `json:"id" jsonschema:"发版记录 ID（必填，先 list_releases 确认）"`
	Status      string `json:"status" jsonschema:"目标状态：preparing|testing|released|rolled_back（必填）"`
	DelegatedBy string `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listReleasesIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
}

type createReviewIn struct {
	Project       string `json:"project" jsonschema:"项目 key（必填）"`
	Kind          string `json:"kind" jsonschema:"评审类型：requirement|release|test（必填）"`
	RequirementID *int64 `json:"requirement_id,omitempty" jsonschema:"关联需求 ID（须已存在）"`
	Conclusion    string `json:"conclusion,omitempty" jsonschema:"pending|passed|passed_with_notes|rejected，缺省 pending"`
	DelegatedBy   string `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listReviewsIn struct {
	Project       string `json:"project" jsonschema:"项目 key（必填）"`
	RequirementID *int64 `json:"requirement_id,omitempty" jsonschema:"按关联需求 ID 过滤"`
}

type listMeetingsIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
}

// ---- 工具实现 ----
//
// 需求三工具。写后 autopush：create 持有 project key 走 autopushKey，
// update 只有实体 ID 走 autopushProject（下同，不再逐个注释）。

func (c *core) createRequirement(_ context.Context, _ *mcp.CallToolRequest, in createRequirementIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, errors.New("必须提供 title")
	}
	if in.Status != "" {
		if err := checkRequirementStatus(in.Status); err != nil {
			return nil, nil, err
		}
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	r := model.Requirement{ProjectID: p.ID, Title: in.Title, Description: in.Desc, Status: in.Status}
	if in.Priority != nil {
		r.Priority = int(*in.Priority)
	}
	if in.Owner != nil && *in.Owner != "" {
		if r.OwnerID, err = c.assigneeWrite(*in.Owner); err != nil {
			return nil, nil, err
		}
	}
	created, err := c.st.CreateRequirement(r, a, behalf) // create 活动由 store 落库
	if err != nil {
		return nil, nil, err
	}
	c.autopushKey(p.Key)
	return jsonText(created)
}

func (c *core) updateRequirement(_ context.Context, _ *mcp.CallToolRequest, in updateRequirementIn) (*mcp.CallToolResult, any, error) {
	var ch store.RequirementChanges
	if in.Status != nil {
		if *in.Status != "" {
			if err := checkRequirementStatus(*in.Status); err != nil {
				return nil, nil, err
			}
		}
		ch.Status = in.Status
	}
	if in.Priority != nil {
		ch.Priority = in.Priority
	}
	if in.Owner != nil {
		if *in.Owner == "" {
			zero := int64(0)
			ch.OwnerID = &zero // 显式空串 = 清空负责人（CLI 同语义）
		} else {
			id, err := c.assigneeWrite(*in.Owner)
			if err != nil {
				return nil, nil, err
			}
			ch.OwnerID = &id
		}
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	updated, err := c.st.UpdateRequirement(in.ID, ch, a, behalf)
	if err != nil {
		return nil, nil, err
	}
	c.autopushProject(updated.ProjectID)
	return jsonText(updated)
}

func (c *core) listRequirements(_ context.Context, _ *mcp.CallToolRequest, in listRequirementsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	rs, err := c.st.ListRequirements(p.ID, in.Status)
	if err != nil {
		return nil, nil, err
	}
	if rs == nil {
		rs = []model.Requirement{} // 稳定输出 []，而非 null
	}
	return jsonText(rs)
}

// bug 三工具。

func (c *core) createBug(_ context.Context, _ *mcp.CallToolRequest, in createBugIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, errors.New("必须提供 title")
	}
	if in.Severity != nil {
		if err := checkBugSeverity(*in.Severity); err != nil {
			return nil, nil, err
		}
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	b := model.Bug{ProjectID: p.ID, Title: in.Title, Description: in.Desc}
	if in.Severity != nil {
		b.Severity = int(*in.Severity)
	}
	if in.Assignee != nil && *in.Assignee != "" {
		if b.AssigneeID, err = c.assigneeWrite(*in.Assignee); err != nil {
			return nil, nil, err
		}
	}
	if in.RequirementID != nil && *in.RequirementID != 0 {
		if err := c.requireLinkedRequirement(*in.RequirementID); err != nil {
			return nil, nil, err
		}
		b.RequirementID = *in.RequirementID
	}
	if in.FoundVersion != "" {
		if b.FoundVersionID, err = c.st.ResolveVersionID(p.ID, in.FoundVersion); err != nil {
			return nil, nil, err
		}
	}
	created, err := c.st.CreateBug(b, a, behalf) // create 活动由 store 落库；reporter 归操作者
	if err != nil {
		return nil, nil, err
	}
	c.autopushKey(p.Key)
	return jsonText(created)
}

func (c *core) updateBug(_ context.Context, _ *mcp.CallToolRequest, in updateBugIn) (*mcp.CallToolResult, any, error) {
	var ch store.BugChanges
	if in.Status != nil {
		if *in.Status != "" {
			if err := checkBugStatus(*in.Status); err != nil {
				return nil, nil, err
			}
		}
		ch.Status = in.Status
	}
	if in.Severity != nil {
		if err := checkBugSeverity(*in.Severity); err != nil {
			return nil, nil, err
		}
		ch.Severity = in.Severity
	}
	if in.Assignee != nil {
		if *in.Assignee == "" {
			zero := int64(0)
			ch.AssigneeID = &zero // 显式空串 = 清空处理人（CLI 同语义）
		} else {
			id, err := c.assigneeWrite(*in.Assignee) // "me" = agent 自己
			if err != nil {
				return nil, nil, err
			}
			ch.AssigneeID = &id
		}
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	updated, err := c.st.UpdateBug(in.ID, ch, a, behalf)
	if err != nil {
		return nil, nil, err
	}
	c.autopushProject(updated.ProjectID)
	return jsonText(updated)
}

func (c *core) listBugs(_ context.Context, _ *mcp.CallToolRequest, in listBugsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	f := store.BugFilter{Status: in.Status}
	if in.Severity != nil {
		f.Severity = *in.Severity
	}
	bs, err := c.st.ListBugs(p.ID, f)
	if err != nil {
		return nil, nil, err
	}
	if bs == nil {
		bs = []model.Bug{}
	}
	return jsonText(bs)
}

// 提测单三工具。

func (c *core) createTestSubmission(_ context.Context, _ *mcp.CallToolRequest, in createTestSubmissionIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	vid, err := c.st.ResolveVersionID(p.ID, in.Version) // 版本必填：空串解析为 0，此处兜底报错
	if err != nil {
		return nil, nil, err
	}
	if vid == 0 {
		return nil, nil, errors.New("必须提供 version（版本 ID 或项目内版本名）")
	}
	t := model.TestSubmission{ProjectID: p.ID, VersionID: vid}
	if in.RequirementID != nil && *in.RequirementID != 0 {
		if err := c.requireLinkedRequirement(*in.RequirementID); err != nil {
			return nil, nil, err
		}
		t.RequirementID = *in.RequirementID
	}
	if in.TestOwner != nil && *in.TestOwner != "" {
		if t.TestOwnerID, err = c.assigneeWrite(*in.TestOwner); err != nil {
			return nil, nil, err
		}
	}
	created, err := c.st.CreateTestSubmission(t, a, behalf) // create 活动由 store 落库；提测人归操作者
	if err != nil {
		return nil, nil, err
	}
	c.autopushKey(p.Key)
	return jsonText(created)
}

func (c *core) updateTestSubmission(_ context.Context, _ *mcp.CallToolRequest, in updateTestSubmissionIn) (*mcp.CallToolResult, any, error) {
	if err := checkSubmissionUpdateStatus(in.Status); err != nil { // draft 不可作为更新目标
		return nil, nil, err
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	status := in.Status
	updated, err := c.st.UpdateTestSubmission(in.ID, store.SubmissionChanges{Status: &status}, a, behalf)
	if err != nil {
		return nil, nil, err
	}
	c.autopushProject(updated.ProjectID)
	return jsonText(updated)
}

func (c *core) listTestSubmissions(_ context.Context, _ *mcp.CallToolRequest, in listTestSubmissionsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	var versionID int64
	if in.Version != "" {
		if versionID, err = c.st.ResolveVersionID(p.ID, in.Version); err != nil {
			return nil, nil, err
		}
	}
	ts, err := c.st.ListTestSubmissions(p.ID, versionID)
	if err != nil {
		return nil, nil, err
	}
	if ts == nil {
		ts = []model.TestSubmission{}
	}
	return jsonText(ts)
}

// 发版三工具。

func (c *core) createRelease(_ context.Context, _ *mcp.CallToolRequest, in createReleaseIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	vid, err := c.st.ResolveVersionID(p.ID, in.Version)
	if err != nil {
		return nil, nil, err
	}
	if vid == 0 {
		return nil, nil, errors.New("必须提供 version（版本 ID 或项目内版本名）")
	}
	r := model.Release{ProjectID: p.ID, VersionID: vid}
	if in.Manager != nil && *in.Manager != "" {
		if r.ReleaseManagerID, err = c.assigneeWrite(*in.Manager); err != nil {
			return nil, nil, err
		}
	}
	created, err := c.st.CreateRelease(r, a, behalf) // create 活动由 store 落库；负责人归操作者
	if err != nil {
		return nil, nil, err
	}
	c.autopushKey(p.Key)
	return jsonText(created)
}

func (c *core) updateRelease(_ context.Context, _ *mcp.CallToolRequest, in updateReleaseIn) (*mcp.CallToolResult, any, error) {
	if err := checkReleaseStatus(in.Status); err != nil {
		return nil, nil, err
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	status := in.Status
	updated, err := c.st.UpdateRelease(in.ID, store.ReleaseChanges{Status: &status}, a, behalf)
	if err != nil {
		return nil, nil, err
	}
	c.autopushProject(updated.ProjectID)
	return jsonText(updated)
}

func (c *core) listReleases(_ context.Context, _ *mcp.CallToolRequest, in listReleasesIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	rs, err := c.st.ListReleases(p.ID)
	if err != nil {
		return nil, nil, err
	}
	if rs == nil {
		rs = []model.Release{}
	}
	return jsonText(rs)
}

// 评审两工具 + 会议只读列表。

func (c *core) createReview(_ context.Context, _ *mcp.CallToolRequest, in createReviewIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	if err := checkReviewKind(in.Kind); err != nil {
		return nil, nil, err
	}
	if in.Conclusion != "" {
		if err := checkReviewConclusion(in.Conclusion); err != nil {
			return nil, nil, err
		}
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	v := model.Review{ProjectID: p.ID, Kind: in.Kind, Conclusion: in.Conclusion}
	if in.RequirementID != nil && *in.RequirementID != 0 {
		if err := c.requireLinkedRequirement(*in.RequirementID); err != nil {
			return nil, nil, err
		}
		v.RequirementID = *in.RequirementID
	}
	created, err := c.st.CreateReview(v, a, behalf) // create 活动由 store 落库；结论缺省 pending
	if err != nil {
		return nil, nil, err
	}
	c.autopushKey(p.Key)
	return jsonText(created)
}

func (c *core) listReviews(_ context.Context, _ *mcp.CallToolRequest, in listReviewsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	var requirementID int64
	if in.RequirementID != nil {
		requirementID = *in.RequirementID
	}
	vs, err := c.st.ListReviews(p.ID, requirementID)
	if err != nil {
		return nil, nil, err
	}
	if vs == nil {
		vs = []model.Review{}
	}
	return jsonText(vs)
}

func (c *core) listMeetings(_ context.Context, _ *mcp.CallToolRequest, in listMeetingsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	ms, err := c.st.ListMeetings(p.ID)
	if err != nil {
		return nil, nil, err
	}
	if ms == nil {
		ms = []model.Meeting{}
	}
	return jsonText(ms)
}
