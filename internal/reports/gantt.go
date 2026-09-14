package reports

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// ganttBar 一条甘特条：位置由全图时间范围归一化为百分比（CSS 绝对定位）。
type ganttBar struct {
	ID        int64
	Title     string
	Status    string
	Start     string
	Due       string
	LeftPct   string // "12.34"（%）
	WidthPct  string
	Deps      string // "依赖 #2、#3"；依赖箭头按 spec 省略，以条下文字标注
	IsDone    bool
	IsDoing   bool
	IsBlocked bool
}

// ganttLane 版本泳道；未绑定版本的任务归入末尾的"未绑定版本"泳道。
type ganttLane struct {
	Name string
	Meta string // 版本目标日期与状态；未绑定版本泳道为空
	Bars []ganttBar
}

type ganttData struct {
	Title            string
	RangeText        string // "2026-09-01 ~ 2026-09-30 · 任务条 5 条"
	Lanes            []ganttLane
	UnscheduledCount int
	Unscheduled      []taskRef
}

// taskRef 极简任务引用（风险区列表用）。
type taskRef struct {
	ID    int64
	Title string
}

// GanttHTML 生成甘特图（自包含单文件 HTML）。只有 start_date 与 due_date
// 都非空（可解析）的任务出条形，按版本分组为泳道；依赖箭头按 spec 省略，
// 以条下文字 "依赖 #<id>" 标注；无排期任务在风险区提示 "未排期任务 N 个"。
// 本报表纯数据驱动、不依赖"今天"，故无需注入 now。
func GanttHTML(s *store.Store, projectID int64) ([]byte, error) {
	p, err := lookupProject(s, projectID)
	if err != nil {
		return nil, err
	}
	versions, err := s.ListVersions(projectID)
	if err != nil {
		return nil, fmt.Errorf("reports: gantt versions: %w", err)
	}
	tasks, err := s.ListTasks(projectID, store.TaskFilter{}) // 默认排除软删
	if err != nil {
		return nil, fmt.Errorf("reports: gantt tasks: %w", err)
	}
	deps, err := s.ListDependencies(projectID)
	if err != nil {
		return nil, fmt.Errorf("reports: gantt dependencies: %w", err)
	}

	// 依赖标注：task → 被依赖任务 ID 列表（升序；重复依赖由 store 唯一性保证）
	depsByTask := map[int64][]int64{}
	for _, d := range deps {
		depsByTask[d.TaskID] = append(depsByTask[d.TaskID], d.DependsOnTaskID)
	}
	for id := range depsByTask {
		ids := depsByTask[id]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}

	type barTask struct {
		task  model.Task
		start time.Time
		due   time.Time
	}
	byVersion := map[int64][]barTask{}
	var unscheduled []taskRef
	for _, t := range tasks {
		sd, okS := parseDate(t.StartDate)
		dd, okD := parseDate(t.DueDate)
		switch {
		case okS && okD:
			bt := barTask{task: t, start: sd, due: dd}
			byVersion[t.VersionID] = append(byVersion[t.VersionID], bt)
		case t.Status != "done": // 缺 start/due 且未完成 → 无排期
			unscheduled = append(unscheduled, taskRef{ID: t.ID, Title: t.Title})
		}
	}

	// place 把一组任务条按全图范围归一化为百分比并排序（开始日期、id）。
	var minT, maxT time.Time
	hasBars := false
	for _, list := range byVersion {
		for _, b := range list {
			if !hasBars || b.start.Before(minT) {
				minT = b.start
			}
			if !hasBars || b.due.After(maxT) {
				maxT = b.due
			}
			hasBars = true
		}
	}
	totalDays := 1
	if hasBars {
		totalDays = int(maxT.Sub(minT).Hours()/24) + 1 // 含首尾两天
	}
	place := func(list []barTask) []ganttBar {
		sort.Slice(list, func(i, j int) bool {
			if !list[i].start.Equal(list[j].start) {
				return list[i].start.Before(list[j].start)
			}
			return list[i].task.ID < list[j].task.ID
		})
		out := make([]ganttBar, 0, len(list))
		for _, b := range list {
			var left, width float64
			if hasBars {
				left = float64(int(b.start.Sub(minT).Hours()/24)) / float64(totalDays) * 100
				span := int(b.due.Sub(b.start).Hours()/24) + 1 // 条形覆盖到 due 当天
				width = float64(span) / float64(totalDays) * 100
			}
			var deps string
			if ids := depsByTask[b.task.ID]; len(ids) > 0 {
				parts := make([]string, len(ids))
				for i, id := range ids {
					parts[i] = fmt.Sprintf("#%d", id)
				}
				deps = "依赖 " + strings.Join(parts, "、")
			}
			out = append(out, ganttBar{
				ID: b.task.ID, Title: b.task.Title, Status: b.task.Status,
				Start: b.task.StartDate, Due: b.task.DueDate,
				LeftPct: pctf(left), WidthPct: pctf(width),
				Deps:      deps,
				IsDone:    b.task.Status == "done",
				IsDoing:   b.task.Status == "in_progress",
				IsBlocked: b.task.Status == "blocked",
			})
		}
		return out
	}

	var lanes []ganttLane
	rangeText := "暂无任务条"
	if hasBars {
		rangeText = fmt.Sprintf("%s ~ %s", minT.Format(layoutDate), maxT.Format(layoutDate))
		for _, v := range versions { // 版本泳道按 id 升序
			if len(byVersion[v.ID]) == 0 {
				continue
			}
			meta := v.Status
			if v.TargetDate != "" {
				meta = "目标 " + v.TargetDate + " · " + v.Status
			}
			lanes = append(lanes, ganttLane{Name: v.Name, Meta: meta, Bars: place(byVersion[v.ID])})
		}
		if len(byVersion[0]) > 0 { // 未绑定版本泳道固定在最后
			lanes = append(lanes, ganttLane{Name: "未绑定版本", Bars: place(byVersion[0])})
		}
	}

	nBars := 0
	for _, l := range lanes {
		nBars += len(l.Bars)
	}
	if nBars > 0 {
		rangeText += fmt.Sprintf(" · 任务条 %d 条", nBars)
	}

	return renderHTML("gantt.html", ganttData{
		Title:            "甘特图 · " + p.Key,
		RangeText:        rangeText,
		Lanes:            lanes,
		UnscheduledCount: len(unscheduled),
		Unscheduled:      unscheduled,
	})
}
