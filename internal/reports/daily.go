package reports

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// 每人日报：按成员生成"今日完成/进行中/明日计划/风险/名下 bug"。
// 数据全部来自本地任务与 activity（确定性生成，无 LLM）；人工感想由成员直接在
// 沉淀文档对应小节下补充，pulse 不管理自由文本。

type dailySection struct {
	Done      []string // 今日完成的任务标题（去重保序）
	InProgess []model.Task
	Planned   []model.Task // 明日到期或明日开始
	Overdue   []model.Task
	Blocked   []model.Task
	Bugs      []model.Bug // 名下未关闭 bug
}

// buildDaily 聚合某成员某天的日报数据。
func buildDaily(s *store.Store, p model.Project, m model.Member, date time.Time) (dailySection, error) {
	var sec dailySection
	day := date.UTC().Truncate(24 * time.Hour)
	next := day.AddDate(0, 0, 1)

	acts, err := s.ActivitiesInWindow(p.ID, day, next)
	if err != nil {
		return sec, fmt.Errorf("reports: daily activities: %w", err)
	}
	doneIDs := doneTaskIDsInWindow(acts)

	tasks, err := s.ListTasks(p.ID, store.TaskFilter{})
	if err != nil {
		return sec, fmt.Errorf("reports: daily tasks: %w", err)
	}
	byID := map[int64]model.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	today := day.Format(layoutDate)
	tomorrow := next.Format(layoutDate)

	for _, t := range tasks {
		if t.Archived || t.AssigneeID != m.ID {
			continue
		}
		if doneIDs[t.ID] {
			sec.Done = append(sec.Done, t.Title)
		}
		switch t.Status {
		case "in_progress":
			sec.InProgess = append(sec.InProgess, t)
		case "blocked":
			sec.Blocked = append(sec.Blocked, t)
		}
		if t.Status != "done" && t.DueDate != "" && t.DueDate < today {
			sec.Overdue = append(sec.Overdue, t)
		}
		if t.Status != "done" && (t.DueDate == tomorrow || t.StartDate == tomorrow) {
			sec.Planned = append(sec.Planned, t)
		}
	}
	open, err := s.ListBugs(p.ID, store.BugFilter{AssigneeID: m.ID})
	if err != nil {
		return sec, fmt.Errorf("reports: daily bugs: %w", err)
	}
	for _, bg := range open {
		if bg.Status != "closed" && bg.Status != "wontfix" {
			sec.Bugs = append(sec.Bugs, bg)
		}
	}
	return sec, nil
}

// DailyMarkdown 生成某成员某天的个人日报（Markdown）。date 取日期部分（UTC）。
func DailyMarkdown(s *store.Store, p model.Project, m model.Member, date time.Time) ([]byte, error) {
	sec, err := buildDaily(s, p, m, date)
	if err != nil {
		return nil, err
	}
	day := date.UTC().Truncate(24 * time.Hour)
	today := day.Format(layoutDate)
	var b strings.Builder
	fmt.Fprintf(&b, "# 日报 · %s · %s\n\n", m.Name, today)

	b.WriteString("## 今日完成\n")
	if len(sec.Done) == 0 {
		b.WriteString("- 无\n")
	}
	for _, title := range sec.Done {
		fmt.Fprintf(&b, "- %s\n", title)
	}

	b.WriteString("\n## 进行中\n")
	writeTaskList(&b, sec.InProgess)

	b.WriteString("\n## 明日计划\n")
	if len(sec.Planned) == 0 {
		b.WriteString("- 无\n")
	}
	for _, t := range sec.Planned {
		ref := t.DueDate
		if ref == "" {
			ref = "开始 " + t.StartDate
		} else {
			ref = "截止 " + ref
		}
		fmt.Fprintf(&b, "- %s（%s）\n", t.Title, ref)
	}

	b.WriteString("\n## 风险\n")
	if len(sec.Overdue) == 0 && len(sec.Blocked) == 0 {
		b.WriteString("- 无\n")
	}
	for _, t := range sec.Overdue {
		fmt.Fprintf(&b, "- 逾期：%s（截止 %s）\n", t.Title, t.DueDate)
	}
	for _, t := range sec.Blocked {
		fmt.Fprintf(&b, "- 阻塞：%s\n", t.Title)
	}

	b.WriteString("\n## 名下 bug\n")
	if len(sec.Bugs) == 0 {
		b.WriteString("- 无\n")
	}
	for _, bg := range sec.Bugs {
		fmt.Fprintf(&b, "- P%d %s（%s）\n", bg.Severity, bg.Title, bg.Status)
	}

	b.WriteString("\n> 由 pulse 按任务数据生成；感想与补充请直接写在本文档对应小节下。\n")
	return []byte(b.String()), nil
}

// AllDailyMarkdown 生成项目全员日报（按成员 id 升序，每人一节）。
func AllDailyMarkdown(s *store.Store, projectID int64, date time.Time) ([]byte, error) {
	p, err := lookupProject(s, projectID)
	if err != nil {
		return nil, err
	}
	members, err := s.ListMembers()
	if err != nil {
		return nil, fmt.Errorf("reports: daily members: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 每日日报 · %s · %s\n\n", p.Name, date.UTC().Format(layoutDate))
	wrote := false
	for _, m := range members {
		body, err := DailyMarkdown(s, p, m, date)
		if err != nil {
			return nil, err
		}
		// 首行"# 日报 · x"降为"## 姓名"；子节"## "降为"### "，保持全员文档的标题层级
		lines := strings.Split(string(body), "\n")
		if len(lines) > 0 {
			lines[0] = "## " + m.Name
			for i := 1; i < len(lines); i++ {
				if strings.HasPrefix(lines[i], "## ") {
					lines[i] = "###" + lines[i][2:]
				}
			}
		}
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
		wrote = true
	}
	if !wrote {
		b.WriteString("（项目暂无成员）\n")
	}
	return []byte(b.String()), nil
}

// doneTaskIDsInWindow 返回窗口内流转为 done 的任务 id 集合（与周报口径一致）。
func doneTaskIDsInWindow(acts []model.Activity) map[int64]bool {
	done := map[int64]bool{}
	for _, a := range acts {
		if a.EntityType != "task" || (a.Action != "update_status" && a.Action != "reopen") {
			continue
		}
		var d struct {
			To string `json:"to"`
		}
		if err := json.Unmarshal([]byte(a.Detail), &d); err != nil || d.To != "done" {
			continue
		}
		done[a.EntityID] = true
	}
	return done
}

func nextDay(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour).AddDate(0, 0, 1)
}

func sortByDueID(ts []model.Task) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].DueDate != ts[j].DueDate {
			return ts[i].DueDate < ts[j].DueDate
		}
		return ts[i].ID < ts[j].ID
	})
}
