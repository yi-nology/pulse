package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// taskStatuses 任务状态合法值（与 store/schema 保持一致），用于校验与提示文案。
var taskStatuses = []string{"backlog", "todo", "in_progress", "blocked", "done"}

func checkTaskStatus(v string) error {
	for _, s := range taskStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(taskStatuses, "|"), v)
}

// checkTaskDate 校验日期参数为 YYYY-MM-DD（空串表示不设置）。
func checkTaskDate(flag, v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return fmt.Errorf("%s 格式须为 YYYY-MM-DD，收到 %q", flag, v)
	}
	return nil
}

// requireProjectFlag 解析并校验 --project，返回项目。
func requireProjectFlag(s *store.Store, key string) (model.Project, error) {
	if key == "" {
		return model.Project{}, fmt.Errorf("必须提供 --project")
	}
	p, found, err := s.GetProjectByKey(key)
	if err != nil {
		return model.Project{}, err
	}
	if !found {
		return model.Project{}, fmt.Errorf("项目不存在: %s", key)
	}
	return p, nil
}

// resolveAssigneeFlag 解析 --assignee：先按名查成员表，命中即用（不论 human/agent，
// 支持"人给 agent 派活"）；未命中再按 human 创建。显式传空串表示清空负责人（返回 0）。
// changed=false 时返回 (nil, 0, nil)。
func resolveAssigneeFlag(s *store.Store, cmd *cobra.Command, name string) (*int64, int64, error) {
	return resolveMemberFlag(s, cmd, "assignee", name)
}

// resolveMemberFlag resolveAssigneeFlag 的按旗标名泛化版，供 --owner/--manager/
// --test-owner 等成员旗标复用同一语义（已有成员直接使用，不存在则按 human 创建）。
func resolveMemberFlag(s *store.Store, cmd *cobra.Command, flagName, name string) (*int64, int64, error) {
	if !cmd.Flags().Changed(flagName) {
		return nil, 0, nil
	}
	if name == "" {
		zero := int64(0)
		return &zero, 0, nil
	}
	m, found, err := s.GetMemberByName(name)
	if err != nil {
		return nil, 0, err
	}
	if !found {
		m, err = s.GetOrCreateMember(name, "human")
		if err != nil {
			return nil, 0, err
		}
	}
	return &m.ID, m.ID, nil
}

// memberNames 全量成员 ID→名映射（成员为全局资源，不分项目；列表命令展示用）。
func memberNames(s *store.Store) (map[int64]string, error) {
	ms, err := s.ListMembers()
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, m := range ms {
		names[m.ID] = m.Name
	}
	return names, nil
}

// newTaskCmd 实现 `pulse task` 子命令组。
func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "任务管理（增删改查，done 任务改回其他状态记 reopen）"}
	cmd.AddCommand(newTaskAddCmd(), newTaskListCmd(), newTaskUpdateCmd(), newTaskRmCmd(), newTaskDepCmd())
	return cmd
}

// newTaskDepCmd 实现 `pulse task dep <id> --on <taskID>`：为任务添加依赖（add_dependency 活动由 store 落库）。
func newTaskDepCmd() *cobra.Command {
	var on string
	cmd := &cobra.Command{
		Use:   "dep <id>",
		Short: "为任务添加依赖（本任务须等 --on 任务完成后才能开始）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("任务 ID 须为整数，收到 %q", args[0])
			}
			onID, err := strconv.ParseInt(on, 10, 64)
			if err != nil {
				return fmt.Errorf("任务 ID 须为整数，收到 %q", on)
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			if err := s.AddDependency(id, onID, a, behalf); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "依赖已添加: 任务 %d 依赖任务 %d\n", id, onID)
			autopushForTask(cmd, s, cfg, id) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&on, "on", "", "被依赖的任务 ID（必填）")
	_ = cmd.MarkFlagRequired("on")
	return cmd
}

// newTaskAddCmd 实现 `pulse task add <title> --project K`。
func newTaskAddCmd() *cobra.Command {
	var projectKey, assignee, status, start, due, version, desc string
	var priority int64
	var estimate float64
	cmd := &cobra.Command{
		Use:   "add <title>",
		Short: "创建任务",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkTaskStatus(status); err != nil {
				return err
			}
			if err := checkTaskDate("--start", start); err != nil {
				return err
			}
			if err := checkTaskDate("--due", due); err != nil {
				return err
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			p, err := requireProjectFlag(s, projectKey)
			if err != nil {
				return err
			}
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			t := model.Task{
				ProjectID: p.ID, Title: args[0], Description: desc,
				Status: status, Priority: int(priority),
				EstimateDays: estimate, StartDate: start, DueDate: due,
			}
			assigneeID, _, err := resolveAssigneeFlag(s, cmd, assignee)
			if err != nil {
				return err
			}
			if assigneeID != nil {
				t.AssigneeID = *assigneeID
			}
			if version != "" {
				vid, err := s.ResolveVersionID(p.ID, version)
				if err != nil {
					return err
				}
				t.VersionID = vid
			}
			tk, err := s.CreateTask(t, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "任务已创建: %s (id=%d)\n", tk.Title, tk.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&assignee, "assignee", "", "负责人成员名（已有成员直接使用，不存在则按 human 创建）")
	cmd.Flags().StringVar(&status, "status", "todo", "任务状态：backlog|todo|in_progress|blocked|done")
	cmd.Flags().Int64Var(&priority, "priority", 3, "优先级（数字越小越优先）")
	cmd.Flags().Float64Var(&estimate, "estimate", 0, "预估人日")
	cmd.Flags().StringVar(&start, "start", "", "开始日期 YYYY-MM-DD")
	cmd.Flags().StringVar(&due, "due", "", "截止日期 YYYY-MM-DD")
	cmd.Flags().StringVar(&version, "version", "", "版本：版本 ID 整数，或项目内版本名（版本名需已创建，versions 表为空时会报错）")
	cmd.Flags().StringVar(&desc, "desc", "", "任务描述")
	return cmd
}

// newTaskListCmd 实现 `pulse task list --project K`，输出列：ID/标题/状态/负责人/截止/版本。
func newTaskListCmd() *cobra.Command {
	var projectKey, status string
	var mine, overdue bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目任务（默认不含已删除任务）",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			p, err := requireProjectFlag(s, projectKey)
			if err != nil {
				return err
			}
			f := store.TaskFilter{Status: status, OverdueOnly: overdue}
			if mine { // "我" = 代理场景下被代表的人类，否则为执行者本人
				a, behalf, err := resolveActor(s, cfg, cmd)
				if err != nil {
					return err
				}
				f.AssigneeID = a.ID
				if behalf != nil {
					f.AssigneeID = behalf.ID
				}
			}
			tasks, err := s.ListTasks(p.ID, f)
			if err != nil {
				return err
			}
			members, err := s.ListMembers()
			if err != nil {
				return err
			}
			memberNames := map[int64]string{}
			for _, m := range members {
				memberNames[m.ID] = m.Name
			}
			versionNames, err := s.VersionNamesByProject(p.ID)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t标题\t状态\t负责人\t截止\t版本")
			for _, t := range tasks {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\t%s\t%s\n",
					t.ID, t.Title, t.Status, memberNames[t.AssigneeID], t.DueDate, versionNames[t.VersionID])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&status, "status", "", "按状态过滤：backlog|todo|in_progress|blocked|done")
	cmd.Flags().BoolVar(&mine, "mine", false, "只看我的任务（当前执行者/被代理人）")
	cmd.Flags().BoolVar(&overdue, "overdue", false, "只看已逾期未完成任务（截止日早于今天 UTC）")
	return cmd
}

// newTaskUpdateCmd 实现 `pulse task update <id>`；只更新显式传入的 flag，
// 活动与时间戳刷新由 store.UpdateTask 在单事务内完成。
func newTaskUpdateCmd() *cobra.Command {
	var title, desc, status, assignee, start, due, version string
	var priority int64
	var estimate float64
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "更新任务（done 改回其他状态记 reopen）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("任务 ID 须为整数，收到 %q", args[0])
			}
			flags := cmd.Flags()
			if flags.Changed("status") {
				if err := checkTaskStatus(status); err != nil {
					return err
				}
			}
			if err := checkTaskDate("--start", start); err != nil {
				return err
			}
			if err := checkTaskDate("--due", due); err != nil {
				return err
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			var ch store.TaskChanges
			if flags.Changed("title") {
				ch.Title = &title
			}
			if flags.Changed("desc") {
				ch.Description = &desc
			}
			if flags.Changed("status") {
				ch.Status = &status
			}
			if flags.Changed("start") {
				ch.StartDate = &start
			}
			if flags.Changed("due") {
				ch.DueDate = &due
			}
			if flags.Changed("priority") {
				ch.Priority = &priority
			}
			if flags.Changed("estimate") {
				ch.EstimateDays = &estimate
			}
			assigneeID, _, err := resolveAssigneeFlag(s, cmd, assignee)
			if err != nil {
				return err
			}
			ch.AssigneeID = assigneeID // nil = 未传，指向 0 = 清空负责人
			if flags.Changed("version") {
				old, found, err := s.GetTask(id)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("任务不存在: id=%d", id)
				}
				vid, err := s.ResolveVersionID(old.ProjectID, version)
				if err != nil {
					return err
				}
				ch.VersionID = &vid
			}
			tk, err := s.UpdateTask(id, ch, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "任务已更新: %s (id=%d)\n", tk.Title, tk.ID)
			autopushForTask(cmd, s, cfg, id) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "新标题")
	cmd.Flags().StringVar(&desc, "desc", "", "任务描述")
	cmd.Flags().StringVar(&status, "status", "", "任务状态：backlog|todo|in_progress|blocked|done")
	cmd.Flags().StringVar(&assignee, "assignee", "", "负责人成员名（已有成员直接使用，不存在则按 human 创建；传空串清空）")
	cmd.Flags().Int64Var(&priority, "priority", 3, "优先级（数字越小越优先）")
	cmd.Flags().Float64Var(&estimate, "estimate", 0, "预估人日")
	cmd.Flags().StringVar(&start, "start", "", "开始日期 YYYY-MM-DD")
	cmd.Flags().StringVar(&due, "due", "", "截止日期 YYYY-MM-DD")
	cmd.Flags().StringVar(&version, "version", "", "版本：版本 ID 整数，或项目内版本名（版本名需已创建，versions 表为空时会报错）；传空串清除版本")
	return cmd
}

// newTaskRmCmd 实现 `pulse task rm <id>`：软删（archived=1，行保留）。
func newTaskRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "删除任务（软删归档，记录保留，默认列表不再显示）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("任务 ID 须为整数，收到 %q", args[0])
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			old, found, err := s.GetTask(id)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("任务不存在: id=%d", id)
			}
			if err := s.SoftDeleteTask(id, a, behalf); err != nil { // archive 活动由 store 落库
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "任务已删除: %s (id=%d)\n", old.Title, old.ID)
			autopushForTask(cmd, s, cfg, id) // 写后自动 push 软删墓碑（尽力而为）
			return nil
		},
	}
}
