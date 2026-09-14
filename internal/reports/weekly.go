// Package reports 生成四份管理结果：甘特图（gantt）、人力负载（workload）、
// 版本规划（versions）、周报（weekly）。零网络、零 LLM，只消费 store 读接口
// 与 rules 纯函数；now 一律由调用方注入以便测试。
//
// 展示口径说明："今天"取 now 注入时刻的 UTC 日期（与 store 落库的 UTC 文本
// 时间、rules 的 overdue/负载窗口一致），不做本地时区换算。
//
// 输出形态：gantt/workload/versions 为自包含单文件 HTML（内联 CSS，无外链、
// 无 JS）；weekly 为 Markdown。golden 测试依赖中文标题文案与排序稳定
// （同数据两次生成必须 byte 级一致，map 迭代一律先排序）。
package reports

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/rules"
	"github.com/zhangyi/pulse/internal/store"
)

// blockedDaysDefault 报表里 blocked 风险的判定阈值（天），与 rules 测试惯用值一致；
// 阈值配置化属后续任务，报表侧先用默认值。
const blockedDaysDefault = 3

const layoutDate = "2006-01-02"

// lookupProject 取项目信息用于报表标题；项目不存在时降级为 "项目 <id>"。
func lookupProject(s *store.Store, projectID int64) (model.Project, error) {
	ps, err := s.ListProjects()
	if err != nil {
		return model.Project{}, fmt.Errorf("reports: list projects: %w", err)
	}
	for _, p := range ps {
		if p.ID == projectID {
			return p, nil
		}
	}
	return model.Project{ID: projectID, Key: fmt.Sprintf("项目 %d", projectID)}, nil
}

// isoWeekWindow 返回 now 所在 ISO 自然周窗口 [周一 00:00 UTC, 下周一 00:00 UTC)。
func isoWeekWindow(now time.Time) (time.Time, time.Time) {
	u := now.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	wd := int(day.Weekday()) // Sunday=0
	if wd == 0 {
		wd = 7
	}
	monday := day.AddDate(0, 0, -(wd - 1))
	return monday, monday.AddDate(0, 0, 7)
}

// doneDetail 周报完成判定只关心 detail 的 to 字段（store.changeDetail 结构）。
type doneDetail struct {
	To string `json:"to"`
}

// completedInWindow 聚合窗口内的完成流转：action ∈ {update_status, reopen}
// 且 detail JSON to=="done" 的 task 活动 → 任务 ID 集合（同任务多次完成去重）。
func completedInWindow(acts []model.Activity) map[int64]bool {
	completed := map[int64]bool{}
	for _, a := range acts {
		if a.EntityType != "task" || (a.Action != "update_status" && a.Action != "reopen") {
			continue
		}
		var d doneDetail
		if err := json.Unmarshal([]byte(a.Detail), &d); err != nil || d.To != "done" {
			continue
		}
		completed[a.EntityID] = true
	}
	return completed
}

// agentContribution 统计窗口内 actor_type='agent' 的活动按 actor 分组计数。
// 计数包含 agent 的全部活动（create/update/update_status/...），反映其总操作量。
func agentContribution(acts []model.Activity, members []model.Member) []agentCount {
	names := make(map[int64]string, len(members))
	for _, m := range members {
		names[m.ID] = m.Name
	}
	counts := map[string]int{}
	for _, a := range acts {
		if a.ActorType != "agent" {
			continue
		}
		name := names[a.ActorID]
		if name == "" {
			name = fmt.Sprintf("成员 %d", a.ActorID)
		}
		counts[name]++
	}
	out := make([]agentCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, agentCount{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type agentCount struct {
	Name  string
	Count int
}

// nextWeekPlan 下周计划：未 done 且 start_date 或 due_date 落在下周自然周
// 窗口 [本周一+7d, 本周一+14d)（UTC，与周报窗口同口径）的任务；
// 按 due 升序（无 due 排后，再按 id），与"进行中"（状态视图）正交可重叠。
func nextWeekPlan(tasks []model.Task, from, to time.Time) []model.Task {
	inNext := func(d time.Time) bool { return !d.Before(from) && d.Before(to) }
	var out []model.Task
	for _, t := range tasks {
		if t.Status == "done" {
			continue
		}
		sd, okS := parseDate(t.StartDate)
		dd, okD := parseDate(t.DueDate)
		if (okS && inNext(sd)) || (okD && inNext(dd)) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, oki := parseDate(out[i].DueDate)
		dj, okj := parseDate(out[j].DueDate)
		if oki != okj {
			return oki // 有 due 的排前面
		}
		if oki && !di.Equal(dj) {
			return di.Before(dj)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// WeeklyMarkdown 生成项目周报（Markdown）：本周完成、进行中、下周计划、
// 风险清单、未排期、Agent 贡献。窗口 = now 所在 ISO 自然周（周一 00:00 UTC 起 7 天）。
func WeeklyMarkdown(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
	p, err := lookupProject(s, projectID)
	if err != nil {
		return nil, err
	}
	weekFrom, weekTo := isoWeekWindow(now)
	nextFrom, nextTo := weekTo, weekTo.AddDate(0, 0, 7)
	acts, err := s.ActivitiesInWindow(projectID, weekFrom, weekTo)
	if err != nil {
		return nil, fmt.Errorf("reports: weekly activities: %w", err)
	}
	completed := completedInWindow(acts)

	tasks, err := s.ListTasks(projectID, store.TaskFilter{}) // 默认排除软删
	if err != nil {
		return nil, fmt.Errorf("reports: weekly tasks: %w", err)
	}
	members, err := s.ListMembers()
	if err != nil {
		return nil, fmt.Errorf("reports: weekly members: %w", err)
	}
	risks, err := rules.Evaluate(s, projectID, now, blockedDaysDefault)
	if err != nil {
		return nil, fmt.Errorf("reports: weekly risks: %w", err)
	}

	var (
		completedTasks, inProgress, unscheduled []model.Task
	)
	for _, t := range tasks {
		switch {
		case completed[t.ID]:
			completedTasks = append(completedTasks, t)
		case t.Status == "in_progress":
			inProgress = append(inProgress, t)
		}
		if t.DueDate == "" && t.Status != "done" {
			unscheduled = append(unscheduled, t)
		}
	}
	agents := agentContribution(acts, members)
	planned := nextWeekPlan(tasks, nextFrom, nextTo)

	var b strings.Builder
	fmt.Fprintf(&b, "# 周报 · %s（%s ~ %s）\n\n", p.Key,
		weekFrom.Format(layoutDate), weekTo.AddDate(0, 0, -1).Format(layoutDate))

	b.WriteString("## 本周完成\n")
	writeTaskList(&b, completedTasks)
	b.WriteString("\n## 进行中\n")
	writeTaskList(&b, inProgress)
	b.WriteString("\n## 下周计划\n")
	writeTaskList(&b, planned)

	b.WriteString("\n## 风险清单\n")
	if len(risks) == 0 {
		b.WriteString("- 无\n")
	}
	for _, r := range risks {
		fmt.Fprintf(&b, "- [%s] %s\n", r.Level, r.Title)
	}

	b.WriteString("\n## 未排期\n")
	writeTaskList(&b, unscheduled)

	b.WriteString("\n## Agent 贡献\n")
	if len(agents) == 0 {
		b.WriteString("- 无\n")
	}
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s: %d 次\n", a.Name, a.Count)
	}
	return []byte(b.String()), nil
}

// writeTaskList 输出 "- #id 标题" 列表；空列表输出 "- 无"。
func writeTaskList(b *strings.Builder, ts []model.Task) {
	if len(ts) == 0 {
		b.WriteString("- 无\n")
		return
	}
	for _, t := range ts {
		fmt.Fprintf(b, "- #%d %s\n", t.ID, t.Title)
	}
}
