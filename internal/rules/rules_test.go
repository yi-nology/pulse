package rules

import (
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// now 注入的"当前时间"：today = 2026-09-15（UTC）。
var now = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func strptr(s string) *string { return &s }

func almostEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// newStore 打开一个独立的临时文件库并返回路径（blocked 用例需要绕过 store API
// 直改 status_changed_at，故须以文件库承载，便于二次打开）。
func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pulse.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func actorOf(t *testing.T, s *store.Store) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember("actor", "human")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// memberOf 建成员并设置每周容量（GetOrCreateMember 落库默认 5）。
func memberOf(t *testing.T, s *store.Store, name string, capacity float64) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember(name, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemberCapacity(m.ID, capacity); err != nil {
		t.Fatal(err)
	}
	m.Capacity = capacity
	return m
}

func projectOf(t *testing.T, s *store.Store) model.Project {
	t.Helper()
	p, err := s.CreateProject("demo", "演示", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// taskOf 按 mut 定制字段后创建任务。
func taskOf(t *testing.T, s *store.Store, projectID int64, actor model.Member, mut func(*model.Task)) model.Task {
	t.Helper()
	mt := model.Task{ProjectID: projectID, Title: "任务"}
	if mut != nil {
		mut(&mt)
	}
	tk, err := s.CreateTask(mt, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func versionOf(t *testing.T, s *store.Store, projectID int64, actor model.Member) model.Version {
	t.Helper()
	v, err := s.CreateVersion(model.Version{ProjectID: projectID, Name: "v1"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func depOf(t *testing.T, s *store.Store, taskID, dependsOnID int64, actor model.Member) {
	t.Helper()
	if err := s.AddDependency(taskID, dependsOnID, actor, nil); err != nil {
		t.Fatal(err)
	}
}

// rewindStatusChangedAt 直改库把任务的 status_changed_at 拨到指定时刻
// （store API 只能写"现在"，无法构造"阻塞了 3 天"的历史现场）。
func rewindStatusChangedAt(t *testing.T, dbPath string, taskID int64, at time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE tasks SET status_changed_at = ? WHERE id = ?`,
		at.UTC().Format("2006-01-02 15:04:05"), taskID); err != nil {
		t.Fatal(err)
	}
}

func risksOfKind(rs []Risk, kind string) []Risk {
	var out []Risk
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestEvaluateNoRisksOnCleanProject(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)

	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 未来到期、有负责人、非阻塞
		m.AssigneeID = actor.ID
		m.DueDate = "2026-09-20"
	})

	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Fatalf("干净项目不应有风险，得到 %+v", rs)
	}
}

func TestEvaluateOverdue(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)

	overdue := taskOf(t, s, p.ID, actor, func(m *model.Task) { // id=1 正例：逾期 2 天
		m.Title = "登录接口"
		m.DueDate = "2026-09-13"
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 反例：done 不报
		m.DueDate = "2026-09-13"
		m.Status = "done"
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 反例：今天到期不算逾期
		m.DueDate = "2026-09-15"
	})
	taskOf(t, s, p.ID, actor, nil)                              // 反例：无到期日不报
	archived := taskOf(t, s, p.ID, actor, func(m *model.Task) { // 反例：archived 排除
		m.DueDate = "2026-09-13"
	})
	if err := s.SoftDeleteTask(archived.ID, actor, nil); err != nil {
		t.Fatal(err)
	}

	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := risksOfKind(rs, "overdue")
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 overdue 风险，得到 %d 条：%+v", len(got), rs)
	}
	r := got[0]
	if r.Level != "high" {
		t.Errorf("overdue Level = %q，期望 high", r.Level)
	}
	if want := "#1 登录接口 逾期 2 天"; r.Title != want {
		t.Errorf("Title = %q，期望 %q", r.Title, want)
	}
	if !strings.Contains(r.Detail, "2 天") || !strings.Contains(r.Detail, "2026-09-13") {
		t.Errorf("Detail 应含逾期天数与截止日，得到 %q", r.Detail)
	}
	_ = overdue
}

func TestEvaluateBlocked(t *testing.T) {
	s, path := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)

	stuck := taskOf(t, s, p.ID, actor, func(m *model.Task) { m.Title = "卡住的接口" })
	taskOf(t, s, p.ID, actor, func(m *model.Task) { m.Title = "刚阻塞" })
	taskOf(t, s, p.ID, actor, func(m *model.Task) { m.Title = "进行中" })
	if _, err := s.UpdateTask(stuck.ID, store.TaskChanges{Status: strptr("blocked")}, actor, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	rewindStatusChangedAt(t, path, stuck.ID, now.Add(-72*time.Hour))   // 阻塞已 3 天
	rewindStatusChangedAt(t, path, stuck.ID+1, now)                    // 刚阻塞，0 天
	rewindStatusChangedAt(t, path, stuck.ID+2, now.Add(-72*time.Hour)) // 非 blocked 状态
	s2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// 正例：阈值 2 天，阻塞 3 天触发；另两条（刚阻塞 / 非 blocked）不触发。
	rs, err := Evaluate(s2, p.ID, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := risksOfKind(rs, "blocked")
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 blocked 风险，得到 %d 条：%+v", len(got), rs)
	}
	r := got[0]
	if r.Level != "high" {
		t.Errorf("blocked Level = %q，期望 high", r.Level)
	}
	if want := "#1 卡住的接口 阻塞 3 天"; r.Title != want {
		t.Errorf("Title = %q，期望 %q", r.Title, want)
	}
	if !strings.Contains(r.Detail, "3 天") {
		t.Errorf("Detail 应含阻塞天数，得到 %q", r.Detail)
	}

	// 反例（边界）：阈值 3 天，"3 天"不满足严格大于，不触发。
	rs, err = Evaluate(s2, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := risksOfKind(rs, "blocked"); len(got) != 0 {
		t.Fatalf("阻塞 3 天在阈值 3 天下不应触发，得到 %+v", got)
	}
}

func TestEvaluateUnassigned(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)
	v := versionOf(t, s, p.ID, actor)
	alice := memberOf(t, s, "alice", 5)

	taskOf(t, s, p.ID, actor, func(m *model.Task) { // id=1 正例：绑了版本但无人
		m.Title = "支付接入"
		m.VersionID = v.ID
	})
	taskOf(t, s, p.ID, actor, nil)                  // 反例：无版本无负责人不报
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 反例：有负责人不报
		m.AssigneeID = alice.ID
		m.VersionID = v.ID
	})

	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := risksOfKind(rs, "unassigned")
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 unassigned 风险，得到 %d 条：%+v", len(got), rs)
	}
	r := got[0]
	if r.Level != "medium" {
		t.Errorf("unassigned Level = %q，期望 medium", r.Level)
	}
	if want := "#1 支付接入 未指定负责人"; r.Title != want {
		t.Errorf("Title = %q，期望 %q", r.Title, want)
	}
}

func TestEvaluateInversion(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)

	a := taskOf(t, s, p.ID, actor, func(m *model.Task) { // id=1 依赖方
		m.Title = "前端联调"
		m.DueDate = "2026-09-20"
	})
	b := taskOf(t, s, p.ID, actor, func(m *model.Task) { // id=2 被依赖方，到期更晚 → 倒置
		m.Title = "后端接口"
		m.DueDate = "2026-09-25"
	})
	c := taskOf(t, s, p.ID, actor, func(m *model.Task) { // id=3 被依赖方，到期更早 → 正常
		m.Title = "数据库迁移"
		m.DueDate = "2026-09-18"
	})
	d := taskOf(t, s, p.ID, actor, nil) // id=4 无到期日 → 不判倒置
	depOf(t, s, a.ID, b.ID, actor)
	depOf(t, s, a.ID, c.ID, actor)
	depOf(t, s, a.ID, d.ID, actor)

	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := risksOfKind(rs, "inversion")
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 inversion 风险，得到 %d 条：%+v", len(got), rs)
	}
	r := got[0]
	if r.Level != "medium" {
		t.Errorf("inversion Level = %q，期望 medium", r.Level)
	}
	if want := "#1 前端联调 依赖倒置"; r.Title != want {
		t.Errorf("Title = %q，期望 %q", r.Title, want)
	}
	if !strings.Contains(r.Detail, "#2") || !strings.Contains(r.Detail, "2026-09-25") {
		t.Errorf("Detail 应指向被依赖任务 #2 及其到期日，得到 %q", r.Detail)
	}
}

func TestWorkloadsAndOverload(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)
	alice := memberOf(t, s, "alice", 1) // 容量 1 人日/周 → 14 天容量 2 人日
	memberOf(t, s, "bob", 5)            // 反例成员：项目内无任务，不返回

	due := "2026-09-18" // today+3，窗口内
	taskOf(t, s, p.ID, actor, func(m *model.Task) {
		m.Title = "接口 A"
		m.AssigneeID = alice.ID
		m.EstimateDays = 2
		m.DueDate = due
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) {
		m.Title = "接口 B"
		m.AssigneeID = alice.ID
		m.EstimateDays = 2
		m.DueDate = due
	})

	ws, err := Workloads(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Fatalf("期望只返回参与项目的 alice，得到 %d 条：%+v", len(ws), ws)
	}
	w := ws[0]
	if w.Member.ID != alice.ID {
		t.Errorf("Member.ID = %d，期望 %d", w.Member.ID, alice.ID)
	}
	if !almostEq(w.LoadDays, 4) {
		t.Errorf("LoadDays = %v，期望 4", w.LoadDays)
	}
	if !almostEq(w.CapacityDays, 2) {
		t.Errorf("CapacityDays = %v，期望 2", w.CapacityDays)
	}
	if !almostEq(w.LoadRate, 2.0) {
		t.Errorf("LoadRate = %v，期望 2.0", w.LoadRate)
	}

	// Evaluate 中的 overload：rate=2.0 > 1.0 → high
	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := risksOfKind(rs, "overload")
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 overload 风险，得到 %d 条：%+v", len(got), rs)
	}
	r := got[0]
	if r.Level != "high" {
		t.Errorf("overload Level = %q，期望 high", r.Level)
	}
	if want := "alice 负载率 200%"; r.Title != want {
		t.Errorf("Title = %q，期望 %q", r.Title, want)
	}
	if !strings.Contains(r.Detail, "4.0") || !strings.Contains(r.Detail, "2.0") {
		t.Errorf("Detail 应含负载与容量人日，得到 %q", r.Detail)
	}
}

func TestWorkloadsRateBelowOne(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)
	carol := memberOf(t, s, "carol", 5) // 14 天容量 10 人日

	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 无到期日：不计入负载率
		m.AssigneeID = carol.ID
		m.EstimateDays = 3
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // done：不计入
		m.AssigneeID = carol.ID
		m.EstimateDays = 2
		m.DueDate = "2026-09-16"
		m.Status = "done"
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 边界：due=today 计入
		m.AssigneeID = carol.ID
		m.EstimateDays = 1
		m.DueDate = "2026-09-15"
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 边界：due=today+14 计入
		m.AssigneeID = carol.ID
		m.EstimateDays = 1.5
		m.DueDate = "2026-09-29"
	})
	taskOf(t, s, p.ID, actor, func(m *model.Task) { // 反例：today+15 窗口外
		m.AssigneeID = carol.ID
		m.EstimateDays = 4
		m.DueDate = "2026-09-30"
	})

	ws, err := Workloads(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Fatalf("期望 1 条负载记录，得到 %d 条：%+v", len(ws), ws)
	}
	w := ws[0]
	if !almostEq(w.LoadDays, 2.5) {
		t.Errorf("LoadDays = %v，期望 2.5", w.LoadDays)
	}
	if !almostEq(w.LoadRate, 0.25) {
		t.Errorf("LoadRate = %v，期望 0.25", w.LoadRate)
	}

	rs, err := Evaluate(s, p.ID, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := risksOfKind(rs, "overload"); len(got) != 0 {
		t.Fatalf("rate=0.25 不应触发 overload，得到 %+v", got)
	}
}

func TestWorkloadsZeroCapacity(t *testing.T) {
	s, _ := newStore(t)
	actor := actorOf(t, s)
	p := projectOf(t, s)
	dave := memberOf(t, s, "dave", 0) // 容量 0 → rate=0，避免除零

	taskOf(t, s, p.ID, actor, func(m *model.Task) {
		m.AssigneeID = dave.ID
		m.EstimateDays = 5
		m.DueDate = "2026-09-16"
	})

	ws, err := Workloads(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Fatalf("期望 1 条负载记录，得到 %d 条：%+v", len(ws), ws)
	}
	if ws[0].LoadRate != 0 {
		t.Errorf("容量为 0 时 LoadRate 应为 0，得到 %v", ws[0].LoadRate)
	}
}
