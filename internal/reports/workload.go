package reports

import (
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/rules"
	"github.com/zhangyi/pulse/internal/store"
)

// workloadTask 计入负载的未完成任务（未来 14 天内到期）。
type workloadTask struct {
	ID       int64
	Title    string
	Due      string
	Estimate string // "1.5"
}

// workloadRow 单成员负载行（人/agent 并列展示）。
type workloadRow struct {
	Name     string
	Type     string
	Load     string // "4.0"（人日）
	Capacity string // "10.0"（人日，周容量 × 2）
	RateText string // "40%"
	BarPct   string // CSS 进度条宽度（封顶 100%）
	Over     bool   // 负载率 > 100%
	Tasks    []workloadTask
}

type workloadData struct {
	Title   string
	Window  string // "未来 14 天（2026-09-15 ~ 2026-09-29）"
	Rows    []workloadRow
	HasRows bool
}

// WorkloadHTML 生成人力负载报表（自包含单文件 HTML）：每 member 的
// 负载人日 vs 容量、负载率，并列出计入负载的任务。口径与 rules 一致：
// 负载 = 未来 14 天内到期且未 done 任务的 estimate_days 之和。
func WorkloadHTML(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
	p, err := lookupProject(s, projectID)
	if err != nil {
		return nil, err
	}
	ws, err := rules.Workloads(s, projectID, now)
	if err != nil {
		return nil, fmt.Errorf("reports: workload: %w", err)
	}
	tasks, err := s.ListTasks(projectID, store.TaskFilter{})
	if err != nil {
		return nil, fmt.Errorf("reports: workload tasks: %w", err)
	}

	t0 := utcToday(now)
	tEnd := t0.AddDate(0, 0, rules.LoadHorizonDays)

	rows := make([]workloadRow, 0, len(ws))
	for _, w := range ws {
		row := workloadRow{
			Name:     w.Member.Name,
			Type:     w.Member.Type,
			Load:     numf(w.LoadDays),
			Capacity: numf(w.CapacityDays),
			RateText: fmt.Sprintf("%.0f%%", w.LoadRate*100),
			BarPct:   pctf(minFloat(w.LoadRate, 1.0) * 100),
			Over:     w.LoadRate > 1.0,
		}
		for _, t := range tasks { // tasks 按 id 升序
			if t.AssigneeID != w.Member.ID || t.Status == "done" {
				continue
			}
			due, ok := parseDate(t.DueDate)
			if !ok || due.Before(t0) || due.After(tEnd) {
				continue // 与 rules.Workloads 的计入口径一致
			}
			row.Tasks = append(row.Tasks, workloadTask{
				ID: t.ID, Title: t.Title, Due: t.DueDate, Estimate: numf(t.EstimateDays),
			})
		}
		rows = append(rows, row)
	}

	return renderHTML("workload.html", workloadData{
		Title:   "人力负载 · " + p.Key,
		Window:  fmt.Sprintf("未来 %d 天（%s ~ %s）", rules.LoadHorizonDays, t0.Format(layoutDate), tEnd.Format(layoutDate)),
		Rows:    rows,
		HasRows: len(rows) > 0,
	})
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
