package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// bugStatuses bug 状态合法值（与 store/schema 保持一致），用于校验与提示文案。
var bugStatuses = []string{"open", "fixing", "fixed", "verified", "closed", "wontfix"}

func checkBugStatus(v string) error {
	for _, s := range bugStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(bugStatuses, "|"), v)
}

// newBugCmd 实现 `pulse bug` 子命令组。
func newBugCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bug", Short: "缺陷管理（severity 1-4 对应 P0-P3，跟踪到关闭）"}
	cmd.AddCommand(newBugAddCmd(), newBugListCmd(), newBugUpdateCmd())
	return cmd
}

// newBugAddCmd 实现 `pulse bug add <title> --project K [--severity 1-4] [--assignee] [--requirement] [--found-version]`。
func newBugAddCmd() *cobra.Command {
	var projectKey, assignee, requirement, foundVersion string
	var severity int64
	cmd := &cobra.Command{
		Use:   "add <title>",
		Short: "创建 bug（severity 缺省 3=P2，status 缺省 open）",
		Args:  cobra.ExactArgs(1),
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
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			b := model.Bug{
				ProjectID: p.ID, Title: args[0], Severity: int(severity),
			}
			assigneeID, _, err := resolveMemberFlag(s, cmd, "assignee", assignee)
			if err != nil {
				return err
			}
			if assigneeID != nil {
				b.AssigneeID = *assigneeID
			}
			if requirement != "" {
				rid, err := strconv.ParseInt(requirement, 10, 64)
				if err != nil {
					return fmt.Errorf("需求 ID 须为整数，收到 %q", requirement)
				}
				// 守卫项目归属：他项目内的同 id 需求同样按不存在拒绝（不泄露存在性）
				req, found, err := s.GetRequirement(rid)
				if err != nil {
					return err
				} else if !found || req.ProjectID != p.ID {
					return fmt.Errorf("需求不存在: id=%d", rid)
				}
				b.RequirementID = rid
			}
			if foundVersion != "" { // 版本 ID 整数，或项目内版本名
				vid, err := s.ResolveVersionID(p.ID, foundVersion)
				if err != nil {
					return err
				}
				b.FoundVersionID = vid
			}
			created, err := s.CreateBug(b, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Bug 已创建: %s (id=%d)\n", created.Title, created.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().Int64Var(&severity, "severity", 0, "严重级 1-4（P0-P3，缺省 3=P2）")
	cmd.Flags().StringVar(&assignee, "assignee", "", "处理人成员名（已有成员直接使用，不存在则按 human 创建）")
	cmd.Flags().StringVar(&requirement, "requirement", "", "关联需求 ID（须已存在）")
	cmd.Flags().StringVar(&foundVersion, "found-version", "", "发现版本：版本 ID 整数，或项目内版本名（版本名需已创建）")
	return cmd
}

// newBugListCmd 实现 `pulse bug list --project K [--status] [--severity]`。
func newBugListCmd() *cobra.Command {
	var projectKey, status string
	var severity int64
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目 bug（--status/--severity 过滤）",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			p, err := requireProjectFlag(s, projectKey)
			if err != nil {
				return err
			}
			bs, err := s.ListBugs(p.ID, store.BugFilter{Status: status, Severity: severity})
			if err != nil {
				return err
			}
			names, err := memberNames(s)
			if err != nil {
				return err
			}
			versionNames, err := s.VersionNamesByProject(p.ID)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t标题\t严重级\t状态\t负责人\t发现版本")
			for _, b := range bs {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%d\t%s\t%s\t%s\n",
					b.ID, b.Title, b.Severity, b.Status, names[b.AssigneeID], versionNames[b.FoundVersionID])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&status, "status", "", "按状态过滤：open|fixing|fixed|verified|closed|wontfix")
	cmd.Flags().Int64Var(&severity, "severity", 0, "按严重级过滤：1-4（P0-P3）")
	return cmd
}

// newBugUpdateCmd 实现 `pulse bug update <id> --status/--assignee/--severity`；
// 只更新显式传入的 flag，活动与校验由 store.UpdateBug 在单事务内完成。
func newBugUpdateCmd() *cobra.Command {
	var status, assignee string
	var severity int64
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "更新 bug（状态变更记 update_status，无 reopen 语义）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("bug ID 须为整数，收到 %q", args[0])
			}
			flags := cmd.Flags()
			if flags.Changed("status") {
				if err := checkBugStatus(status); err != nil {
					return err
				}
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
			var ch store.BugChanges
			if flags.Changed("status") {
				ch.Status = &status
			}
			if flags.Changed("severity") {
				ch.Severity = &severity
			}
			assigneeID, _, err := resolveMemberFlag(s, cmd, "assignee", assignee)
			if err != nil {
				return err
			}
			ch.AssigneeID = assigneeID // nil = 未传，指向 0 = 清空处理人
			b, err := s.UpdateBug(id, ch, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Bug 已更新: %s (id=%d)\n", b.Title, b.ID)
			autopushForEntityProject(cmd, s, cfg, b.ProjectID) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "bug 状态：open|fixing|fixed|verified|closed|wontfix")
	cmd.Flags().StringVar(&assignee, "assignee", "", "处理人成员名（已有成员直接使用，不存在则按 human 创建；传空串清空）")
	cmd.Flags().Int64Var(&severity, "severity", 0, "严重级 1-4（P0-P3）")
	return cmd
}
