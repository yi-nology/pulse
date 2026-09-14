// Package mcpserver 把 pulse 核心能力（store + rules）封装为 MCP 工具，
// 供编码代理（Claude Code / Codex / ZCode 等）经 `pulse mcp`（stdio 传输）驱动。
//
// 约定（task-9 简报与 spec §5.2）：
//   - 全部工具返回 JSON 字符串：统一 json.MarshalIndent(v, "", "  ")（两空格缩进，
//     代理与人读均友好）；工具内错误通过 error 返回，由 go-sdk 打包为
//     isError=true 的工具结果（LLM 可见、可自纠），不上升为协议错误；
//   - 每个工具的 description 都包含规定句 requiredDesc，降低 agent 误操作；
//   - actor 语义：agentName（来自 PULSE_ACTOR）对应的 agent 成员是操作执行者
//     （首次调用 get-or-create），delegated_by 参数解析为代表的人类（behalf）；
//   - New(s, agentName) 内部读一次 config（PULSE_HOME 可重定向）取 default_actor
//     传给 Register；测试可直接调 Register 注入依赖；
//   - publish_feishu 经 PublishReportFunc 钩子装配，本包零飞书依赖（除 stub）：
//     Task 13 实现 internal/feishu 后在 cli/mcp.go 注入；钩子为 nil 时返回引导错误文本。
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/rules"
	"github.com/zhangyi/pulse/internal/store"
)

// requiredDesc 每个工具 description 必须包含的提示句（spec §5.2）。
const requiredDesc = "更新任务前先 list_tasks 确认 id"

// blockedDaysDefault get_project_status 里 blocked 风险的判定阈值（天），
// 与 internal/reports 的同名常量保持一致。
const blockedDaysDefault = 3

// serverVersion 随二进制注入前的固定占位（spec 未约定版本号上报格式）。
const serverVersion = "dev"

// PublishReportFunc 是 publish_feishu 的注入点：Task 13 实现 internal/feishu
// 的 PublishReport 后由 cli/mcp.go 装配（保持 mcpserver 零飞书依赖）。
// 返回值将作为 JSON 结果回给代理。nil 时 publish_feishu 返回引导错误文本。
var PublishReportFunc func(st *store.Store, projectKey, report string) (any, error)

// New 组装 pulse MCP server。default_actor 经 config.Load(config.DefaultPath())
// 读取一次（PULSE_HOME 可重定向），供 delegated_by 的 behalf 解析使用；
// 配置读取失败必须中止启动，因此返回 error。
func New(s *store.Store, agentName string) (*mcp.Server, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "pulse", Version: serverVersion}, nil)
	Register(srv, s, agentName, cfg.DefaultActor)
	return srv, nil
}

// Register 把全部工具注册到 srv；New 内部调用，测试也可直接注入依赖。
func Register(srv *mcp.Server, s *store.Store, agentName, defaultActor string) {
	c := &core{st: s, agentName: agentName, defaultActor: defaultActor}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_projects",
		Description: "列出全部项目。" + requiredDesc}, c.listProjects)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_project_status",
		Description: "项目元数据 + 未完成任务数 + 风险摘要（overdue/blocked/unassigned/inversion/overload）。" + requiredDesc}, c.getProjectStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_tasks",
		Description: `按项目列出任务，支持 assignee/status/version/overdue 过滤；assignee="me" 解析为当前 agent 自己。` + requiredDesc}, c.listTasks)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_task",
		Description: "创建任务。" + requiredDesc}, c.addTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_task",
		Description: "更新任务（done 改回其他状态自动记 reopen）。" + requiredDesc}, c.updateTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_dependency",
		Description: "为任务添加完成-开始（FS）依赖。" + requiredDesc}, c.addDependency)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_versions",
		Description: "列出项目内版本。" + requiredDesc}, c.listVersions)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_version",
		Description: "创建版本（项目内版本名唯一，缺省 planned）。" + requiredDesc}, c.addVersion)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_version",
		Description: "更新版本状态/目标日期/备注。" + requiredDesc}, c.updateVersion)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_members",
		Description: "列出全部成员（human 与 agent）。" + requiredDesc}, c.listMembers)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_workload",
		Description: "项目成员未来 14 天负载（到期未完成人日 / 容量）。" + requiredDesc}, c.getWorkload)
	mcp.AddTool(srv, &mcp.Tool{Name: "publish_feishu",
		Description: "把报表（weekly|versions|all）沉淀到飞书文档。" + requiredDesc}, c.publishFeishu)
}

// core 聚合一次工具调用所需的依赖；方法即各工具实现。
type core struct {
	st           *store.Store
	agentName    string
	defaultActor string
}

// resolve 解析本次调用执行者：agent 成员（get-or-create）+ delegated_by 对应的 behalf。
func (c *core) resolve(delegatedBy string) (model.Member, *model.Member, error) {
	return actor.Resolve(c.st, c.defaultActor, c.agentName, delegatedBy)
}

// project 解析必填的 project 参数（按 key 查询）。
func (c *core) project(key string) (model.Project, error) {
	if strings.TrimSpace(key) == "" {
		return model.Project{}, errors.New("必须提供 project")
	}
	p, found, err := c.st.GetProjectByKey(key)
	if err != nil {
		return model.Project{}, err
	}
	if !found {
		return model.Project{}, fmt.Errorf("项目不存在: %s", key)
	}
	return p, nil
}

// jsonText 统一结果序列化：两空格缩进 JSON 文本作为唯一 content。
func jsonText(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal tool result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

// checkStatus 校验任务状态合法值（与 schema CHECK 一致）。
func checkStatus(v string) error {
	for _, s := range []string{"backlog", "todo", "in_progress", "blocked", "done"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 backlog|todo|in_progress|blocked|done，收到 %q", v)
}

// checkVersionStatus 校验版本状态合法值。
func checkVersionStatus(v string) error {
	for _, s := range []string{"planned", "in_dev", "released", "shipped"} {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 planned|in_dev|released|shipped，收到 %q", v)
}

// checkDate 校验日期参数为 YYYY-MM-DD（空串合法）。
func checkDate(field, v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return fmt.Errorf("%s 格式须为 YYYY-MM-DD，收到 %q", field, v)
	}
	return nil
}

// ---- 工具入参（struct 字段即 JSON schema；普通字段 + 无 omitempty = 必填）----

type getProjectStatusIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
}

type listTasksIn struct {
	Project  string `json:"project" jsonschema:"项目 key（必填）"`
	Assignee string `json:"assignee,omitempty" jsonschema:"负责人成员名；me 表示当前 agent 自己"`
	Status   string `json:"status,omitempty" jsonschema:"backlog|todo|in_progress|blocked|done"`
	Version  string `json:"version,omitempty" jsonschema:"版本 ID 或项目内版本名"`
	Overdue  *bool  `json:"overdue,omitempty" jsonschema:"true 时仅看已逾期未完成任务"`
}

type addTaskIn struct {
	Project      string   `json:"project" jsonschema:"项目 key（必填）"`
	Title        string   `json:"title" jsonschema:"任务标题（必填）"`
	Assignee     *string  `json:"assignee,omitempty" jsonschema:"负责人成员名（不存在则按 human 创建）"`
	Status       string   `json:"status,omitempty" jsonschema:"backlog|todo|in_progress|blocked|done，缺省 todo"`
	Priority     *int64   `json:"priority,omitempty" jsonschema:"优先级，数字越小越优先，缺省 3"`
	EstimateDays *float64 `json:"estimate_days,omitempty" jsonschema:"预估人日"`
	Start        string   `json:"start,omitempty" jsonschema:"开始日期 YYYY-MM-DD"`
	Due          string   `json:"due,omitempty" jsonschema:"截止日期 YYYY-MM-DD"`
	Version      string   `json:"version,omitempty" jsonschema:"版本 ID 或项目内版本名"`
	Desc         string   `json:"desc,omitempty" jsonschema:"任务描述"`
	DelegatedBy  string   `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名（activity 记 on_behalf_of）"`
}

// updateTaskIn 与 add_task 可选字段同表；另补 title/desc（CLI task update 同语义）。
// 指针字段区分"未传"与"传空串清空"（assignee/version 空串即清空）。
type updateTaskIn struct {
	ID           int64    `json:"id" jsonschema:"任务 ID（必填，先 list_tasks 确认）"`
	Title        *string  `json:"title,omitempty" jsonschema:"新标题"`
	Desc         *string  `json:"desc,omitempty" jsonschema:"任务描述"`
	Assignee     *string  `json:"assignee,omitempty" jsonschema:"负责人成员名（不存在则按 human 创建）；空串清空"`
	Status       *string  `json:"status,omitempty" jsonschema:"backlog|todo|in_progress|blocked|done"`
	Priority     *int64   `json:"priority,omitempty" jsonschema:"优先级，数字越小越优先"`
	EstimateDays *float64 `json:"estimate_days,omitempty" jsonschema:"预估人日"`
	Start        *string  `json:"start,omitempty" jsonschema:"开始日期 YYYY-MM-DD"`
	Due          *string  `json:"due,omitempty" jsonschema:"截止日期 YYYY-MM-DD"`
	Version      *string  `json:"version,omitempty" jsonschema:"版本 ID 或项目内版本名；空串清除版本"`
	DelegatedBy  string   `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type addDependencyIn struct {
	TaskID          int64  `json:"task_id" jsonschema:"任务 ID（必填）"`
	DependsOnTaskID int64  `json:"depends_on_task_id" jsonschema:"被依赖的任务 ID（必填）"`
	DelegatedBy     string `json:"delegated_by,omitempty" jsonschema:"agent 代表执行的人类成员名"`
}

type listVersionsIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
}

type addVersionIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
	Name    string `json:"name" jsonschema:"版本名（必填，项目内唯一）"`
	Target  string `json:"target,omitempty" jsonschema:"目标日期 YYYY-MM-DD"`
	Status  string `json:"status,omitempty" jsonschema:"planned|in_dev|released|shipped，缺省 planned"`
	Notes   string `json:"notes,omitempty" jsonschema:"备注"`
}

type updateVersionIn struct {
	ID     int64   `json:"id" jsonschema:"版本 ID（必填）"`
	Status *string `json:"status,omitempty" jsonschema:"planned|in_dev|released|shipped"`
	Target *string `json:"target,omitempty" jsonschema:"目标日期 YYYY-MM-DD"`
	Notes  *string `json:"notes,omitempty" jsonschema:"备注"`
}

type getWorkloadIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
}

type publishFeishuIn struct {
	Project string `json:"project" jsonschema:"项目 key（必填）"`
	Report  string `json:"report" jsonschema:"weekly|versions|all（必填）"`
}

// ---- 结果视图 ----

// taskView 在 model.Task 上补 assignee/version 的人读名称（与 CLI 列表列对齐）。
// 内嵌结构体无 json tag 时字段按原名展开（ID/Title/...）。
type taskView struct {
	model.Task
	AssigneeName string `json:"assignee_name,omitempty"`
	VersionName  string `json:"version_name,omitempty"`
}

type riskView struct {
	Kind  string `json:"kind"`
	Level string `json:"level"`
	Title string `json:"title"`
}

type projectStatusView struct {
	Project   model.Project `json:"project"`
	OpenTasks int           `json:"open_tasks"`
	Risks     []riskView    `json:"risks"`
}

type workloadView struct {
	MemberID     int64   `json:"member_id"`
	MemberName   string  `json:"member_name"`
	MemberType   string  `json:"member_type"`
	LoadDays     float64 `json:"load_days"`
	CapacityDays float64 `json:"capacity_days"`
	LoadRate     float64 `json:"load_rate"`
}

// ---- 工具实现 ----

func (c *core) listProjects(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
	ps, err := c.st.ListProjects()
	if err != nil {
		return nil, nil, err
	}
	if ps == nil {
		ps = []model.Project{} // 稳定输出 []，而非 null
	}
	return jsonText(ps)
}

func (c *core) getProjectStatus(_ context.Context, _ *mcp.CallToolRequest, in getProjectStatusIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	tasks, err := c.st.ListTasks(p.ID, store.TaskFilter{})
	if err != nil {
		return nil, nil, err
	}
	open := 0
	for _, t := range tasks {
		if t.Status != "done" {
			open++
		}
	}
	risks, err := rules.Evaluate(c.st, p.ID, time.Now(), blockedDaysDefault)
	if err != nil {
		return nil, nil, err
	}
	views := make([]riskView, 0, len(risks))
	for _, r := range risks {
		views = append(views, riskView{Kind: r.Kind, Level: r.Level, Title: r.Title})
	}
	return jsonText(projectStatusView{Project: p, OpenTasks: open, Risks: views})
}

func (c *core) listTasks(_ context.Context, _ *mcp.CallToolRequest, in listTasksIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	f := store.TaskFilter{Status: in.Status, OverdueOnly: in.Overdue != nil && *in.Overdue}
	if in.Assignee != "" {
		f.AssigneeID, err = c.assigneeFilter(in.Assignee)
		if err != nil {
			return nil, nil, err
		}
	}
	if in.Version != "" {
		f.VersionID, err = c.st.ResolveVersionID(p.ID, in.Version)
		if err != nil {
			return nil, nil, err
		}
	}
	tasks, err := c.st.ListTasks(p.ID, f)
	if err != nil {
		return nil, nil, err
	}
	views, err := c.taskViews(p.ID, tasks)
	if err != nil {
		return nil, nil, err
	}
	return jsonText(views)
}

// assigneeFilter list 过滤用成员解析："me" → agent 自己；其余按名查询（只查不建，
// 列表动作不应有创建成员的副作用）。
func (c *core) assigneeFilter(name string) (int64, error) {
	if name == "me" {
		a, _, err := c.resolve("")
		if err != nil {
			return 0, err
		}
		return a.ID, nil
	}
	ms, err := c.st.ListMembers()
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.Name == name {
			return m.ID, nil
		}
	}
	return 0, fmt.Errorf("成员不存在: %s", name)
}

// taskViews 为任务列表补人读名称；保证返回非 nil 空数组。
func (c *core) taskViews(projectID int64, tasks []model.Task) ([]taskView, error) {
	members, err := c.st.ListMembers()
	if err != nil {
		return nil, err
	}
	memberNames := make(map[int64]string, len(members))
	for _, m := range members {
		memberNames[m.ID] = m.Name
	}
	versionNames, err := c.st.VersionNamesByProject(projectID)
	if err != nil {
		return nil, err
	}
	views := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, taskView{
			Task:         t,
			AssigneeName: memberNames[t.AssigneeID],
			VersionName:  versionNames[t.VersionID],
		})
	}
	return views, nil
}

// taskView 单个任务的视图（add/update 返回用）。
func (c *core) singleTaskView(t model.Task) (*mcp.CallToolResult, any, error) {
	views, err := c.taskViews(t.ProjectID, []model.Task{t})
	if err != nil {
		return nil, nil, err
	}
	return jsonText(views[0])
}

func (c *core) addTask(_ context.Context, _ *mcp.CallToolRequest, in addTaskIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, errors.New("必须提供 title")
	}
	if in.Status != "" {
		if err := checkStatus(in.Status); err != nil {
			return nil, nil, err
		}
	}
	if err := checkDate("start", in.Start); err != nil {
		return nil, nil, err
	}
	if err := checkDate("due", in.Due); err != nil {
		return nil, nil, err
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	t := model.Task{
		ProjectID: p.ID, Title: in.Title, Description: in.Desc, Status: in.Status,
		StartDate: in.Start, DueDate: in.Due,
	}
	if in.Priority != nil {
		t.Priority = int(*in.Priority)
	}
	if in.EstimateDays != nil {
		t.EstimateDays = *in.EstimateDays
	}
	if in.Assignee != nil && *in.Assignee != "" { // 与 CLI add 一致：空串 = 不设负责人
		m, err := c.st.GetOrCreateMember(*in.Assignee, "human")
		if err != nil {
			return nil, nil, err
		}
		t.AssigneeID = m.ID
	}
	if in.Version != "" {
		t.VersionID, err = c.st.ResolveVersionID(p.ID, in.Version)
		if err != nil {
			return nil, nil, err
		}
	}
	created, err := c.st.CreateTask(t, a, behalf) // create 活动由 store 落库
	if err != nil {
		return nil, nil, err
	}
	return c.singleTaskView(created)
}

func (c *core) updateTask(_ context.Context, _ *mcp.CallToolRequest, in updateTaskIn) (*mcp.CallToolResult, any, error) {
	var ch store.TaskChanges
	if in.Title != nil {
		ch.Title = in.Title
	}
	if in.Desc != nil {
		ch.Description = in.Desc
	}
	if in.Status != nil {
		if *in.Status != "" {
			if err := checkStatus(*in.Status); err != nil {
				return nil, nil, err
			}
		}
		ch.Status = in.Status
	}
	if in.Start != nil {
		if err := checkDate("start", *in.Start); err != nil {
			return nil, nil, err
		}
		ch.StartDate = in.Start
	}
	if in.Due != nil {
		if err := checkDate("due", *in.Due); err != nil {
			return nil, nil, err
		}
		ch.DueDate = in.Due
	}
	if in.Priority != nil {
		ch.Priority = in.Priority
	}
	if in.EstimateDays != nil {
		ch.EstimateDays = in.EstimateDays
	}
	if in.Assignee != nil {
		if *in.Assignee == "" {
			zero := int64(0)
			ch.AssigneeID = &zero // 显式空串 = 清空负责人（CLI 同语义）
		} else {
			m, err := c.st.GetOrCreateMember(*in.Assignee, "human")
			if err != nil {
				return nil, nil, err
			}
			ch.AssigneeID = &m.ID
		}
	}
	if in.Version != nil {
		old, _, err := c.st.GetTask(in.ID) // 版本须按任务所属项目解析
		if err != nil {
			return nil, nil, err
		}
		vid, err := c.st.ResolveVersionID(old.ProjectID, *in.Version)
		if err != nil {
			return nil, nil, err
		}
		ch.VersionID = &vid // ResolveVersionID("") → 0 → 清除版本
	}
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	updated, err := c.st.UpdateTask(in.ID, ch, a, behalf) // reopen 语义由 store 自动处理
	if err != nil {
		return nil, nil, err
	}
	return c.singleTaskView(updated)
}

func (c *core) addDependency(_ context.Context, _ *mcp.CallToolRequest, in addDependencyIn) (*mcp.CallToolResult, any, error) {
	a, behalf, err := c.resolve(in.DelegatedBy)
	if err != nil {
		return nil, nil, err
	}
	if err := c.st.AddDependency(in.TaskID, in.DependsOnTaskID, a, behalf); err != nil {
		return nil, nil, err
	}
	return jsonText(map[string]any{
		"ok": true, "task_id": in.TaskID, "depends_on_task_id": in.DependsOnTaskID, "type": "FS",
	})
}

func (c *core) listVersions(_ context.Context, _ *mcp.CallToolRequest, in listVersionsIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	vs, err := c.st.ListVersions(p.ID)
	if err != nil {
		return nil, nil, err
	}
	if vs == nil {
		vs = []model.Version{}
	}
	return jsonText(vs)
}

func (c *core) addVersion(_ context.Context, _ *mcp.CallToolRequest, in addVersionIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, errors.New("必须提供 name")
	}
	if in.Status != "" {
		if err := checkVersionStatus(in.Status); err != nil {
			return nil, nil, err
		}
	}
	if err := checkDate("target", in.Target); err != nil {
		return nil, nil, err
	}
	a, behalf, err := c.resolve("")
	if err != nil {
		return nil, nil, err
	}
	v, err := c.st.CreateVersion(model.Version{
		ProjectID: p.ID, Name: in.Name, TargetDate: in.Target, Status: in.Status, Notes: in.Notes,
	}, a, behalf) // create 活动由 store 落库
	if err != nil {
		return nil, nil, err
	}
	return jsonText(v)
}

func (c *core) updateVersion(_ context.Context, _ *mcp.CallToolRequest, in updateVersionIn) (*mcp.CallToolResult, any, error) {
	var ch store.VersionChanges
	if in.Status != nil {
		if *in.Status != "" {
			if err := checkVersionStatus(*in.Status); err != nil {
				return nil, nil, err
			}
		}
		ch.Status = in.Status
	}
	if in.Target != nil {
		if err := checkDate("target", *in.Target); err != nil {
			return nil, nil, err
		}
		ch.TargetDate = in.Target
	}
	if in.Notes != nil {
		ch.Notes = in.Notes
	}
	a, behalf, err := c.resolve("")
	if err != nil {
		return nil, nil, err
	}
	v, err := c.st.UpdateVersion(in.ID, ch, a, behalf)
	if err != nil {
		return nil, nil, err
	}
	return jsonText(v)
}

func (c *core) listMembers(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
	ms, err := c.st.ListMembers()
	if err != nil {
		return nil, nil, err
	}
	if ms == nil {
		ms = []model.Member{}
	}
	return jsonText(ms)
}

func (c *core) getWorkload(_ context.Context, _ *mcp.CallToolRequest, in getWorkloadIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	ws, err := rules.Workloads(c.st, p.ID, time.Now())
	if err != nil {
		return nil, nil, err
	}
	views := make([]workloadView, 0, len(ws))
	for _, w := range ws {
		views = append(views, workloadView{
			MemberID: w.Member.ID, MemberName: w.Member.Name, MemberType: w.Member.Type,
			LoadDays: w.LoadDays, CapacityDays: w.CapacityDays, LoadRate: w.LoadRate,
		})
	}
	return jsonText(views)
}

func (c *core) publishFeishu(_ context.Context, _ *mcp.CallToolRequest, in publishFeishuIn) (*mcp.CallToolResult, any, error) {
	p, err := c.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	switch in.Report {
	case "weekly", "versions", "all":
	default:
		return nil, nil, fmt.Errorf("report 必须为 weekly|versions|all，收到 %q", in.Report)
	}
	if PublishReportFunc == nil {
		return nil, nil, errors.New("未配置飞书：请先在 ~/.pulse/config.yaml 配置 feishu.app_id/app_secret" +
			"（或设置环境变量 PULSE_FEISHU_APP_SECRET），再执行 pulse feishu bind 绑定项目后重试；" +
			"本地报表不受影响，可使用 pulse report weekly|versions|all")
	}
	out, err := PublishReportFunc(c.st, p.Key, in.Report)
	if err != nil {
		return nil, nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return jsonText(out)
}
