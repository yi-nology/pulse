package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// requirementStatuses 需求状态合法值（与 store/schema 保持一致），用于校验与提示文案。
var requirementStatuses = []string{"proposed", "reviewing", "accepted", "in_dev", "delivered", "rejected"}

func checkRequirementStatus(v string) error {
	for _, s := range requirementStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(requirementStatuses, "|"), v)
}

// newRequirementCmd 实现 `pulse requirement` 子命令组。
func newRequirementCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "requirement", Short: "需求管理（proposed 到 delivered 的生命周期流转）"}
	cmd.AddCommand(newRequirementAddCmd(), newRequirementListCmd(), newRequirementUpdateCmd(), newRequirementDocCmd())
	return cmd
}

// newRequirementAddCmd 实现 `pulse requirement add <title> --project K [--owner] [--priority] [--status proposed]`。
func newRequirementAddCmd() *cobra.Command {
	var projectKey, owner, status string
	var priority int64
	cmd := &cobra.Command{
		Use:   "add <title>",
		Short: "创建需求（status 缺省 proposed，priority 缺省 3）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRequirementStatus(status); err != nil {
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
			r := model.Requirement{
				ProjectID: p.ID, Title: args[0], Status: status, Priority: int(priority),
			}
			ownerID, _, err := resolveMemberFlag(s, cmd, "owner", owner)
			if err != nil {
				return err
			}
			if ownerID != nil {
				r.OwnerID = *ownerID
			}
			created, err := s.CreateRequirement(r, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "需求已创建: %s (id=%d)\n", created.Title, created.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&owner, "owner", "", "需求负责人成员名（已有成员直接使用，不存在则按 human 创建）")
	cmd.Flags().Int64Var(&priority, "priority", 3, "优先级（数字越小越优先）")
	cmd.Flags().StringVar(&status, "status", "proposed", "需求状态：proposed|reviewing|accepted|in_dev|delivered|rejected")
	return cmd
}

// newRequirementListCmd 实现 `pulse requirement list --project K [--status]`。
func newRequirementListCmd() *cobra.Command {
	var projectKey, status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目需求（--status 按状态过滤）",
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
			rs, err := s.ListRequirements(p.ID, status)
			if err != nil {
				return err
			}
			memberNames, err := memberNames(s)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t标题\t状态\t负责人\t优先级")
			for _, r := range rs {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\t%d\n",
					r.ID, r.Title, r.Status, memberNames[r.OwnerID], r.Priority)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&status, "status", "", "按状态过滤：proposed|reviewing|accepted|in_dev|delivered|rejected")
	return cmd
}

// newRequirementUpdateCmd 实现 `pulse requirement update <id> --status/--owner/--priority`；
// 只更新显式传入的 flag，活动与校验由 store.UpdateRequirement 在单事务内完成。
func newRequirementUpdateCmd() *cobra.Command {
	var status, owner string
	var priority int64
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "更新需求（状态变更记 update_status，无 reopen 语义）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("需求 ID 须为整数，收到 %q", args[0])
			}
			flags := cmd.Flags()
			if flags.Changed("status") {
				if err := checkRequirementStatus(status); err != nil {
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
			var ch store.RequirementChanges
			if flags.Changed("status") {
				ch.Status = &status
			}
			if flags.Changed("priority") {
				ch.Priority = &priority
			}
			ownerID, _, err := resolveMemberFlag(s, cmd, "owner", owner)
			if err != nil {
				return err
			}
			ch.OwnerID = ownerID // nil = 未传，指向 0 = 清空负责人
			r, err := s.UpdateRequirement(id, ch, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "需求已更新: %s (id=%d)\n", r.Title, r.ID)
			autopushForEntityProject(cmd, s, cfg, r.ProjectID) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "需求状态：proposed|reviewing|accepted|in_dev|delivered|rejected")
	cmd.Flags().StringVar(&owner, "owner", "", "需求负责人成员名（已有成员直接使用，不存在则按 human 创建；传空串清空）")
	cmd.Flags().Int64Var(&priority, "priority", 3, "优先级（数字越小越优先）")
	return cmd
}

// newRequirementDocCmd 实现 `pulse requirement doc <id>`。
// TODO(v1.1-task3): EnsureRecordDoc 接线点——Task 3 落地飞书模板文档绑定后改为真实输出。
func newRequirementDocCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doc <id>",
		Short: "查看需求绑定的协作文档（Task 3 接入飞书模板文档）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("需求 ID 须为整数，收到 %q", args[0])
			}
			s, _, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			if _, found, err := s.GetRequirement(id); err != nil {
				return err
			} else if !found {
				return fmt.Errorf("需求不存在: id=%d", id)
			}
			// TODO(v1.1-task3): EnsureRecordDoc 接线点
			fmt.Fprintln(cmd.OutOrStdout(), "文档未绑定（v1.1 Task 3 提供飞书模板文档）")
			return nil
		},
	}
}
