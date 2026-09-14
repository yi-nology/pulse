package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// versionStatuses 版本状态合法值（与 store/schema 保持一致），用于校验与提示文案。
var versionStatuses = []string{"planned", "in_dev", "released", "shipped"}

func checkVersionStatus(v string) error {
	for _, s := range versionStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(versionStatuses, "|"), v)
}

// newVersionCmd 实现 `pulse version` 子命令组。
func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "version", Short: "版本管理（项目内版本名唯一）"}
	cmd.AddCommand(newVersionAddCmd(), newVersionListCmd(), newVersionUpdateCmd())
	return cmd
}

// newVersionAddCmd 实现 `pulse version add <name> --project K [--target 2026-10-01]`。
func newVersionAddCmd() *cobra.Command {
	var projectKey, target string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "创建版本（项目内版本名唯一，status 缺省 planned）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkTaskDate("--target", target); err != nil {
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
			v, err := s.CreateVersion(model.Version{
				ProjectID: p.ID, Name: args[0], TargetDate: target,
			}, a, behalf) // create 活动由 store 落库；重复名错误文案由 store 提供
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "版本已创建: %s (id=%d)\n", v.Name, v.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&target, "target", "", "目标日期 YYYY-MM-DD")
	return cmd
}

// newVersionListCmd 实现 `pulse version list --project K`。
func newVersionListCmd() *cobra.Command {
	var projectKey string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目版本",
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
			vs, err := s.ListVersions(p.ID)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t名称\t状态\t目标日期\t备注")
			for _, v := range vs {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\t%s\n", v.ID, v.Name, v.Status, v.TargetDate, v.Notes)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	return cmd
}

// newVersionUpdateCmd 实现 `pulse version update <id> --status in_dev|released|shipped [--target]`；
// 只更新显式传入的 flag，活动与校验由 store.UpdateVersion 在单事务内完成。
func newVersionUpdateCmd() *cobra.Command {
	var status, target string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "更新版本（状态变更记 update_status，版本无 reopen 语义）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("版本 ID 须为整数，收到 %q", args[0])
			}
			flags := cmd.Flags()
			if flags.Changed("status") {
				if err := checkVersionStatus(status); err != nil {
					return err
				}
			}
			if err := checkTaskDate("--target", target); err != nil {
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
			var ch store.VersionChanges
			if flags.Changed("status") {
				ch.Status = &status
			}
			if flags.Changed("target") {
				ch.TargetDate = &target
			}
			v, err := s.UpdateVersion(id, ch, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "版本已更新: %s (id=%d)\n", v.Name, v.ID)
			autopushForVersion(cmd, s, cfg, id) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "版本状态：planned|in_dev|released|shipped")
	cmd.Flags().StringVar(&target, "target", "", "目标日期 YYYY-MM-DD")
	return cmd
}
