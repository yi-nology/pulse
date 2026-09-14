// Package rules 实现纯函数形式的项目风险引擎：从 store 读取任务/依赖/成员，
// 按 spec 计算口径输出风险列表与成员负载。本包只依赖 store/model/标准库，
// 无 LLM、无网络调用；now 由调用方注入以便测试。
//
// Level 约定（简报仅明确 overload=high，其余按此默认并在包注释备案）：
//
//	overdue=high、blocked=high、unassigned=medium、inversion=medium、overload=high。
//
// 计算口径（全部排除 archived）：
//   - overdue：due_date < today(now) 且 status != done；
//   - blocked：status=blocked 且 now-status_changed_at > blockedDays 天（严格大于）；
//   - unassigned：assignee=0 且 version_id != 0；
//   - inversion：对每条依赖 A depends_on B，两者都有 due 且 B.due > A.due；
//   - 负载率 = 未来 14 天内到期且未 done 任务的 estimate_days 之和 ÷（capacity_days_per_week × 2），
//     到期窗口为 [today, today+14] 闭区间；无到期日任务不计入负载率；
//   - overload：Workload.LoadRate > 1.0；CapacityDays=0 时 LoadRate=0（不触发）。
package rules

import (
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// RiskKind / RiskLevel 的合法取值。
const (
	KindOverdue    = "overdue"
	KindBlocked    = "blocked"
	KindUnassigned = "unassigned"
	KindInversion  = "inversion"
	KindOverload   = "overload"

	LevelHigh   = "high"
	LevelMedium = "medium"
)

const (
	layoutDate          = "2006-01-02"
	layoutStatusChanged = "2006-01-02 15:04:05" // 与 store 的 UTC 文本格式一致
	loadHorizonDays     = 14                    // 负载统计窗口（天）
)

// Risk 一条人读风险。
type Risk struct {
	Kind   string // overdue | blocked | unassigned | inversion | overload
	Level  string // high | medium
	Title  string // 人读摘要，如 "#3 登录接口 逾期 2 天"
	Detail string
}

// Workload 单个成员未来 14 天的负载。
type Workload struct {
	Member       model.Member
	LoadDays     float64 // 未来 14 天内到期未完成任务的 estimate 之和
	CapacityDays float64 // capacity_days_per_week × 2
	LoadRate     float64 // LoadDays / CapacityDays；Capacity=0 时为 0
}

// Evaluate 返回项目的全部风险。blockedDays 为 blocked 判定阈值（天）；
// now 由调用方注入便于测试，today 取 now 的 UTC 日期。
func Evaluate(s *store.Store, projectID int64, now time.Time, blockedDays int) ([]Risk, error) {
	tasks, err := s.ListTasks(projectID, store.TaskFilter{}) // 默认排除 archived
	if err != nil {
		return nil, fmt.Errorf("rules: list tasks: %w", err)
	}
	deps, err := s.ListDependencies(projectID)
	if err != nil {
		return nil, fmt.Errorf("rules: list dependencies: %w", err)
	}
	members, err := s.ListMembers()
	if err != nil {
		return nil, fmt.Errorf("rules: list members: %w", err)
	}
	return risksFrom(tasks, deps, workloadsFrom(tasks, members, now), now, blockedDays), nil
}

// Workloads 返回参与项目（在项目内有任务）的成员的负载，按成员在任务中
// 首次出现的顺序（任务 id 升序）排列。
func Workloads(s *store.Store, projectID int64, now time.Time) ([]Workload, error) {
	tasks, err := s.ListTasks(projectID, store.TaskFilter{})
	if err != nil {
		return nil, fmt.Errorf("rules: list tasks: %w", err)
	}
	members, err := s.ListMembers()
	if err != nil {
		return nil, fmt.Errorf("rules: list members: %w", err)
	}
	return workloadsFrom(tasks, members, now), nil
}

// risksFrom 纯计算：overdue/blocked/unassigned 按任务 id 升序逐条判定，
// 其后是 inversion（依赖 id 升序）、overload（成员负载顺序）。
func risksFrom(tasks []model.Task, deps []model.Dependency, ws []Workload, now time.Time, blockedDays int) []Risk {
	t0 := today(now)
	byID := make(map[int64]model.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}

	var risks []Risk
	for _, t := range tasks {
		if t.Status != "done" {
			if due, ok := parseDate(t.DueDate); ok && due.Before(t0) {
				days := int(t0.Sub(due).Hours() / 24)
				risks = append(risks, Risk{
					Kind:  KindOverdue,
					Level: LevelHigh,
					Title: fmt.Sprintf("#%d %s 逾期 %d 天", t.ID, t.Title, days),
					Detail: fmt.Sprintf("任务 #%d「%s」截止 %s，已逾期 %d 天，当前状态 %s",
						t.ID, t.Title, t.DueDate, days, t.Status),
				})
			}
		}
		if t.Status == "blocked" {
			if changed, ok := parseStatusChangedAt(t.StatusChangedAt); ok {
				if d := now.Sub(changed); d.Hours() > float64(blockedDays)*24 {
					days := int(d.Hours() / 24)
					risks = append(risks, Risk{
						Kind:  KindBlocked,
						Level: LevelHigh,
						Title: fmt.Sprintf("#%d %s 阻塞 %d 天", t.ID, t.Title, days),
						Detail: fmt.Sprintf("任务 #%d「%s」自 %s 起 blocked 已 %d 天，超过阈值 %d 天",
							t.ID, t.Title, t.StatusChangedAt, days, blockedDays),
					})
				}
			}
		}
		if t.AssigneeID == 0 && t.VersionID != 0 {
			risks = append(risks, Risk{
				Kind:  KindUnassigned,
				Level: LevelMedium,
				Title: fmt.Sprintf("#%d %s 未指定负责人", t.ID, t.Title),
				Detail: fmt.Sprintf("任务 #%d「%s」已绑定版本 #%d 但未指定负责人",
					t.ID, t.Title, t.VersionID),
			})
		}
	}

	for _, d := range deps {
		a, okA := byID[d.TaskID]
		b, okB := byID[d.DependsOnTaskID]
		if !okA || !okB {
			continue
		}
		dueA, okA := parseDate(a.DueDate)
		dueB, okB := parseDate(b.DueDate)
		if !okA || !okB || !dueB.After(dueA) {
			continue
		}
		risks = append(risks, Risk{
			Kind:  KindInversion,
			Level: LevelMedium,
			Title: fmt.Sprintf("#%d %s 依赖倒置", a.ID, a.Title),
			Detail: fmt.Sprintf("#%d（due %s）依赖 #%d（due %s），被依赖任务到期晚于依赖方",
				a.ID, a.DueDate, b.ID, b.DueDate),
		})
	}

	for _, w := range ws {
		if w.LoadRate > 1.0 {
			risks = append(risks, Risk{
				Kind:  KindOverload,
				Level: LevelHigh,
				Title: fmt.Sprintf("%s 负载率 %.0f%%", w.Member.Name, w.LoadRate*100),
				Detail: fmt.Sprintf("%s 未来 %d 天到期未完成任务共 %.1f 人日，容量 %.1f 人日（%.1f 人日/周 × %d）",
					w.Member.Name, loadHorizonDays, w.LoadDays, w.CapacityDays,
					w.Member.Capacity, 2),
			})
		}
	}
	return risks
}

// workloadsFrom 纯计算：仅统计在项目内承担过任务的成员（参与项目的成员）；
// done 任务与无到期日任务不计入 LoadDays；CapacityDays=0 时 LoadRate=0。
func workloadsFrom(tasks []model.Task, members []model.Member, now time.Time) []Workload {
	t0 := today(now)
	tEnd := t0.AddDate(0, 0, loadHorizonDays)
	byID := make(map[int64]model.Member, len(members))
	for _, m := range members {
		byID[m.ID] = m
	}

	var ws []Workload
	seen := make(map[int64]bool)
	for _, t := range tasks { // tasks 按 id 升序 → 结果按成员首次出现顺序
		if t.AssigneeID == 0 || seen[t.AssigneeID] {
			continue
		}
		m, ok := byID[t.AssigneeID]
		if !ok { // assignee 指向已不存在的成员，忽略
			continue
		}
		seen[t.AssigneeID] = true

		var load float64
		for _, tt := range tasks {
			if tt.AssigneeID != m.ID || tt.Status == "done" {
				continue
			}
			due, ok := parseDate(tt.DueDate)
			if !ok || due.Before(t0) || due.After(tEnd) { // 无到期日 / 窗口外不计入
				continue
			}
			load += tt.EstimateDays
		}
		capacity := m.Capacity * 2
		var rate float64
		if capacity > 0 {
			rate = load / capacity
		}
		ws = append(ws, Workload{Member: m, LoadDays: load, CapacityDays: capacity, LoadRate: rate})
	}
	return ws
}

// today 返回 now 的 UTC 零点。
func today(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// parseDate 解析 due_date（YYYY-MM-DD）；空串或非法格式返回 false
// （视为无到期日，不参与 overdue / 负载 / 倒置判定）。
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(layoutDate, s)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// parseStatusChangedAt 解析 status_changed_at（store 写入的 UTC 文本格式）。
func parseStatusChangedAt(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(layoutStatusChanged, s)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
