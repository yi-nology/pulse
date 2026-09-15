package reports

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/rules"
	"github.com/zhangyi/pulse/internal/store"
)

// riskKindLabels 风险类型的中文标签；kindOrder 固定摘要展示顺序（golden 稳定）。
var riskKindLabels = map[string]string{
	rules.KindOverdue:    "逾期",
	rules.KindBlocked:    "阻塞",
	rules.KindUnassigned: "未指定负责人",
	rules.KindInversion:  "依赖倒置",
	rules.KindOverload:   "超载",
}

var riskKindOrder = []string{
	rules.KindOverdue, rules.KindBlocked, rules.KindUnassigned, rules.KindInversion, rules.KindOverload,
}

// versionTask 版本 scope 内的任务行。
type versionTask struct {
	ID        int64
	Title     string
	Assignee  string
	Status    string
	Due       string
	Estimate  string
	IsOverdue bool
	IsDone    bool
}

// versionBlock 单版本的规划视图。
type versionBlock struct {
	Name        string
	Status      string
	TargetDate  string
	Total       int
	Done        int
	DonePct     string // 完成度 CSS 宽度（%）
	DonePctText string // "60%"
	OverdueN    int
	Tasks       []versionTask
	RiskSummary string // "逾期 1 · 依赖倒置 1"；无风险为 "无"
	RiskLines   []string
}

type versionsData struct {
	Title      string
	Versions   []versionBlock
	HasVersion bool
	OtherRiskN int
	OtherRisks []string // 无法归属到版本的风险（超载、未绑定版本任务的风险）
}

// riskTaskID 从风险标题前缀 "#<id> " 解析归属任务 ID（rules 的 Title 约定）。
func riskTaskID(title string) (int64, bool) {
	if !strings.HasPrefix(title, "#") {
		return 0, false
	}
	rest := title[1:]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(rest[:i], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// VersionsHTML 生成版本规划报表（自包含单文件 HTML）：每版本的 scope、
// 完成度、逾期项、目标日期，以及按版本归属的风险摘要（风险按 rules.Evaluate
// 全量计算后，依据标题 "#<id>" 前缀归属到任务所在版本；超载等无法归属的
// 汇入"其他风险"）。
func VersionsHTML(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
	p, blocks, otherRisks, err := collectVersions(s, projectID, now)
	if err != nil {
		return nil, err
	}
	return renderHTML("versions.html", versionsData{
		Title:      "版本规划 · " + p.Key,
		Versions:   blocks,
		HasVersion: len(blocks) > 0,
		OtherRiskN: len(otherRisks),
		OtherRisks: otherRisks,
	})
}

// collectVersions 是版本规划报表的统一数据源（VersionsHTML 与 VersionsText 共用，
// 保证两种形态口径一致）：项目信息 + 每版本的 scope/完成度/逾期/风险摘要 +
// 无法归属到版本的其他风险。
func collectVersions(s *store.Store, projectID int64, now time.Time) (model.Project, []versionBlock, []string, error) {
	p, err := lookupProject(s, projectID)
	if err != nil {
		return model.Project{}, nil, nil, err
	}
	versions, err := s.ListVersions(projectID)
	if err != nil {
		return model.Project{}, nil, nil, fmt.Errorf("reports: versions list: %w", err)
	}
	tasks, err := s.ListTasks(projectID, store.TaskFilter{})
	if err != nil {
		return model.Project{}, nil, nil, fmt.Errorf("reports: versions tasks: %w", err)
	}
	risks, err := rules.Evaluate(s, projectID, now, rules.BlockedDaysDefault)
	if err != nil {
		return model.Project{}, nil, nil, fmt.Errorf("reports: versions risks: %w", err)
	}
	members, err := s.ListMembers()
	if err != nil {
		return model.Project{}, nil, nil, fmt.Errorf("reports: versions members: %w", err)
	}

	memberNames := make(map[int64]string, len(members))
	for _, m := range members {
		memberNames[m.ID] = m.Name
	}

	// 风险归属：taskID → versionID
	taskVersion := make(map[int64]int64, len(tasks))
	for _, t := range tasks {
		taskVersion[t.ID] = t.VersionID
	}
	riskCounts := map[int64]map[string]int{} // versionID → kind → count
	riskLines := map[int64][]string{}        // versionID → 风险标题（保持 Evaluate 顺序）
	var otherRisks []string
	for _, r := range risks {
		line := "[" + r.Level + "] " + r.Title
		if id, ok := riskTaskID(r.Title); ok {
			if vid := taskVersion[id]; vid != 0 {
				if riskCounts[vid] == nil {
					riskCounts[vid] = map[string]int{}
				}
				riskCounts[vid][r.Kind]++
				riskLines[vid] = append(riskLines[vid], line)
				continue
			}
		}
		otherRisks = append(otherRisks, line)
	}

	t0 := utcToday(now)
	blocks := make([]versionBlock, 0, len(versions))
	for _, v := range versions { // 版本按 id 升序
		blk := versionBlock{Name: v.Name, Status: v.Status, TargetDate: v.TargetDate}
		var overdue []taskRef
		for _, t := range tasks { // id 升序
			if t.VersionID != v.ID {
				continue
			}
			vt := versionTask{
				ID: t.ID, Title: t.Title, Status: t.Status, Due: t.DueDate,
				Assignee: memberNames[t.AssigneeID], Estimate: numf(t.EstimateDays),
				IsDone: t.Status == "done",
			}
			if !vt.IsDone {
				if due, ok := parseDate(t.DueDate); ok && due.Before(t0) {
					vt.IsOverdue = true
					overdue = append(overdue, taskRef{ID: t.ID, Title: t.Title})
				}
			}
			blk.Total++
			if vt.IsDone {
				blk.Done++
			}
			blk.Tasks = append(blk.Tasks, vt)
		}
		blk.OverdueN = len(overdue)
		rate := 0.0
		if blk.Total > 0 {
			rate = float64(blk.Done) / float64(blk.Total)
		}
		blk.DonePct = pctf(rate * 100)
		blk.DonePctText = fmt.Sprintf("%.0f%%", rate*100)
		blk.RiskSummary = summarizeRisks(riskCounts[v.ID])
		blk.RiskLines = riskLines[v.ID]
		blocks = append(blocks, blk)
	}
	return p, blocks, otherRisks, nil
}

// summarizeRisks 按固定 kind 顺序输出非零计数摘要；全零为 "无"。
func summarizeRisks(counts map[string]int) string {
	var parts []string
	for _, kind := range riskKindOrder {
		if n := counts[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", riskKindLabels[kind], n))
		}
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, " · ")
}
