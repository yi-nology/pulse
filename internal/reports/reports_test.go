package reports

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// now 注入的"当前时间"：today = 2026-09-15（UTC，周二），所在 ISO 周 = 09-14（一）~ 09-20（日）。
var now = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

// newStore 打开独立的临时文件库（与 rules 测试同构）。
func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func memberOf(t *testing.T, s *store.Store, name, typ string) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember(name, typ)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func projectOf(t *testing.T, s *store.Store) model.Project {
	t.Helper()
	p, err := s.CreateProject("demo", "演示项目", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func taskOf(t *testing.T, s *store.Store, projectID int64, mut func(*model.Task)) model.Task {
	t.Helper()
	mt := model.Task{ProjectID: projectID, Title: "任务"}
	if mut != nil {
		mut(&mt)
	}
	tk, err := s.CreateTask(mt, actorOf(t, s), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// actorOf 取公共人类执行者 alice（get-or-create 幂等，各测试独立库）。
func actorOf(t *testing.T, s *store.Store) model.Member {
	t.Helper()
	return memberOf(t, s, "alice", "human")
}

// logTaskActivity 直写一条 task 活动（CreatedAt 显式指定，构造窗口内/外的历史现场）。
func logTaskActivity(t *testing.T, s *store.Store, projectID int64, actor model.Member, action string, taskID int64, detail, createdAt string) {
	t.Helper()
	if err := s.LogActivity(model.Activity{
		ProjectID: projectID, ActorID: actor.ID, ActorType: actor.Type,
		Action: action, EntityType: "task", EntityID: taskID,
		Detail: detail, CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// statusDetail 构造状态流转明细（与 store.changeDetail 同构）。
func statusDetail(from, to string) string {
	return `{"field":"status","from":"` + from + `","to":"` + to + `"}`
}

// section 按 "## 标题" 切出 Markdown 小节正文（到下一个小节或文末），便于分节断言。
func section(md, title string) string {
	lines := strings.Split(md, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(ln, "## ")) == title
			continue
		}
		if in {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// TestWeeklyCompletionAndAgentContribution 周报两个核心口径：
//   - "本周完成"只认窗口内 action∈{update_status,reopen} 且 detail to=="done" 的活动，按任务去重；
//   - "agent 贡献"按 actor_type='agent' 的窗口内活动按 actor 计数。
func TestWeeklyCompletionAndAgentContribution(t *testing.T) {
	s := newStore(t)
	p := projectOf(t, s)
	alice := memberOf(t, s, "alice", "human")
	bot := memberOf(t, s, "bot", "agent")

	// #1 本周内 update_status → done（完成者 alice）
	t1 := taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "本周完成A" })
	logTaskActivity(t, s, p.ID, alice, "update_status", t1.ID, statusDetail("in_progress", "done"), "2026-09-15 08:00:00")

	// #2 窗口内 action=update（非状态流转）即使 to=="done" 也不计
	t2 := taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "非状态活动不计" })
	logTaskActivity(t, s, p.ID, alice, "update", t2.ID, statusDetail("todo", "done"), "2026-09-15 09:00:00")

	// #3 窗口内 reopen 但 to=in_progress（返工）不计完成
	t3 := taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "返工不计完成" })
	logTaskActivity(t, s, p.ID, alice, "reopen", t3.ID, statusDetail("done", "in_progress"), "2026-09-16 09:00:00")

	// #4 窗口外（上周）的 done 不计
	t4 := taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "上周完成" })
	logTaskActivity(t, s, p.ID, alice, "update_status", t4.ID, statusDetail("in_progress", "done"), "2026-09-10 08:00:00")

	// #5 本周完成两次（done→todo→done）只计一次
	t5 := taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "重复完成去重" })
	logTaskActivity(t, s, p.ID, bot, "update_status", t5.ID, statusDetail("todo", "done"), "2026-09-14 12:00:00")
	logTaskActivity(t, s, p.ID, bot, "update_status", t5.ID, statusDetail("done", "todo"), "2026-09-14 13:00:00")
	logTaskActivity(t, s, p.ID, bot, "update_status", t5.ID, statusDetail("todo", "done"), "2026-09-15 14:00:00")

	// #6 进行中（未完成）
	taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "进行中任务"; m.Status = "in_progress" })

	// #7 未排期（无 due 且未完成）
	taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "未排期任务" })

	// #8 逾期风险（due < today 且未 done）
	taskOf(t, s, p.ID, func(m *model.Task) { m.Title = "逾期任务"; m.DueDate = "2026-09-10" })

	// agent 贡献：窗口内 bot 共 2 条活动（上面的 #5 两条；#5 第三条也是 bot 的，共 3 条）
	// —— 精确计数：bot 在窗口内 3 条 update_status。

	md, err := WeeklyMarkdown(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	out := string(md)

	done := section(out, "本周完成")
	for _, want := range []string{"#1 本周完成A", "#5 重复完成去重"} {
		if !strings.Contains(done, want) {
			t.Fatalf("本周完成 must contain %q, got:\n%s", want, done)
		}
	}
	for _, notWant := range []string{"#2", "#3", "#4", "非状态活动不计", "返工不计完成", "上周完成"} {
		if strings.Contains(done, notWant) {
			t.Fatalf("本周完成 must NOT contain %q, got:\n%s", notWant, done)
		}
	}
	if n := strings.Count(done, "#5 重复完成去重"); n != 1 {
		t.Fatalf("本周完成 must dedup #5, got %d occurrences:\n%s", n, done)
	}

	if !strings.Contains(section(out, "进行中"), "#6 进行中任务") {
		t.Fatalf("进行中 must contain #6, got:\n%s", section(out, "进行中"))
	}
	if !strings.Contains(section(out, "未排期"), "#7 未排期任务") {
		t.Fatalf("未排期 must contain #7, got:\n%s", section(out, "未排期"))
	}
	if strings.Contains(section(out, "未排期"), "#8 逾期任务") {
		t.Fatalf("未排期 must NOT contain #8（有 due，属逾期风险而非未排期），got:\n%s", section(out, "未排期"))
	}
	if !strings.Contains(section(out, "风险清单"), "逾期") {
		t.Fatalf("风险清单 must contain 逾期 risk, got:\n%s", section(out, "风险清单"))
	}
	agent := section(out, "Agent 贡献")
	if !strings.Contains(agent, "bot: 3 次") {
		t.Fatalf("Agent 贡献 must contain %q, got:\n%s", "bot: 3 次", agent)
	}
	if strings.Contains(agent, "alice") {
		t.Fatalf("Agent 贡献 must not count human alice, got:\n%s", agent)
	}
}

// ---- golden 测试 ----

var updateGolden = flag.Bool("update", false, "重新生成 golden 文件（go test ./internal/reports/ -update）")

// checkGolden 对比 golden 文件；-update 时重写。
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("golden", name)
	if *updateGolden {
		if err := os.MkdirAll("golden", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden updated: %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden %s 失败（首次请运行 go test ./internal/reports/ -update）: %v", name, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("输出与 golden %s 不一致（数据/文案变更需确认后用 -update 重新生成）:\n--- want ---\n%s\n--- got ---\n%s",
			name, want, got)
	}
}

// goldenFixture 构造覆盖四份报表各分支的确定性数据集（now = 2026-09-15 10:00 UTC）：
//   - #1 搭建骨架        v1.0  09-01~09-10  done      alice  est 3   （本周完成，bot 代完成）
//   - #2 登录接口        v1.0  09-08~09-20  in_progress alice est 3
//   - #3 依赖倒置示例    v1.0  09-10~09-18  todo      bob    est 2   （依赖 #5 → 倒置）
//   - #4 接口文档        v2.0  无start due 09-25     todo   bob  est 1
//   - #5 环境准备        v1.0  无start due 09-25     todo   无主   est 2   （未指定负责人风险）
//   - #6 数据迁移        v2.0  无任何日期            todo   无主   est 0   （未排期）
//   - #7 修复登录超时    v1.0  09-12~09-16  in_progress bob  est 1
//   - #8 历史欠账        v2.0  无start due 09-05     todo   bob  est 2   （逾期风险）
//
// 负载（窗口 09-15~09-29）：alice 3/10（30%）；bob #3+#4+#7 = 4/2（200%，超载）。
// 依赖倒置：#3（due 09-18）依赖 #5（due 09-25）。
func goldenFixture(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s := newStore(t)
	alice := memberOf(t, s, "alice", "human")
	bob := memberOf(t, s, "bob", "human")
	bot := memberOf(t, s, "bot", "agent")
	if err := s.SetMemberCapacity(bob.ID, 1); err != nil {
		t.Fatal(err)
	}
	p := projectOf(t, s)
	actor := actorOf(t, s)

	v1, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", TargetDate: "2026-09-30", Status: "in_dev"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v2.0", TargetDate: "2026-10-15", Status: "planned"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	mk := func(title, status, start, due string, assignee int64, version int64, est float64) model.Task {
		tk, err := s.CreateTask(model.Task{
			ProjectID: p.ID, Title: title, Status: status, StartDate: start, DueDate: due,
			AssigneeID: assignee, VersionID: version, EstimateDays: est,
		}, actor, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	t1 := mk("搭建骨架", "done", "2026-09-01", "2026-09-10", alice.ID, v1.ID, 3)
	t2 := mk("登录接口", "in_progress", "2026-09-08", "2026-09-20", alice.ID, v1.ID, 3)
	t3 := mk("依赖倒置示例", "todo", "2026-09-10", "2026-09-18", bob.ID, v1.ID, 2)
	mk("接口文档", "todo", "", "2026-09-25", bob.ID, v2.ID, 1)
	t5 := mk("环境准备", "todo", "", "2026-09-25", 0, v1.ID, 2)
	mk("数据迁移", "todo", "", "", 0, v2.ID, 0)
	mk("修复登录超时", "in_progress", "2026-09-12", "2026-09-16", bob.ID, v1.ID, 1)
	mk("历史欠账", "todo", "", "2026-09-05", bob.ID, v2.ID, 2)

	if err := s.AddDependency(t3.ID, t5.ID, actor, nil); err != nil {
		t.Fatal(err)
	}

	// 窗口内活动（CreatedAt 显式固定）：
	logTaskActivity(t, s, p.ID, bot, "update_status", t1.ID, statusDetail("in_progress", "done"), "2026-09-15 09:00:00")
	logTaskActivity(t, s, p.ID, bot, "update_status", t2.ID, statusDetail("todo", "in_progress"), "2026-09-15 10:00:00")
	logTaskActivity(t, s, p.ID, alice, "update", t3.ID, `{"field":"due_date","from":"2026-09-17","to":"2026-09-18"}`, "2026-09-16 09:00:00")
	return s, p.ID
}

func TestGoldenGantt(t *testing.T) {
	s, pid := goldenFixture(t)
	got, err := GanttHTML(s, pid)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "gantt.html", got)
}

func TestGoldenWorkload(t *testing.T) {
	s, pid := goldenFixture(t)
	got, err := WorkloadHTML(s, pid, now)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "workload.html", got)
}

func TestGoldenVersions(t *testing.T) {
	s, pid := goldenFixture(t)
	got, err := VersionsHTML(s, pid, now)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "versions.html", got)
}

func TestGoldenWeekly(t *testing.T) {
	s, pid := goldenFixture(t)
	got, err := WeeklyMarkdown(s, pid, now)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "weekly.md", got)
}

// TestReportsDeterministic 同一数据两次独立生成必须 byte 级一致
// （golden 稳定性的运行时保障：map 迭代顺序一律排序，禁止注入墙钟时间）。
func TestReportsDeterministic(t *testing.T) {
	build := func() [][]byte {
		s, pid := goldenFixture(t)
		var out [][]byte
		for _, f := range []func() ([]byte, error){
			func() ([]byte, error) { return GanttHTML(s, pid) },
			func() ([]byte, error) { return WorkloadHTML(s, pid, now) },
			func() ([]byte, error) { return VersionsHTML(s, pid, now) },
			func() ([]byte, error) { return WeeklyMarkdown(s, pid, now) },
		} {
			b, err := f()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		return out
	}
	first := build()
	second := build()
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("report %d 非确定性：两次生成结果不一致\n--- first ---\n%s\n--- second ---\n%s", i, first[i], second[i])
		}
	}
}
