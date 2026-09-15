package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// releaseStatuses 发版状态合法值（与 store/schema 保持一致），用于校验与提示文案。
var releaseStatuses = []string{"preparing", "testing", "released", "rolled_back"}

func checkReleaseStatus(v string) error {
	for _, s := range releaseStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(releaseStatuses, "|"), v)
}

// newReleaseCmd 实现 `pulse release` 子命令组。
func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "release", Short: "发版记录（preparing 到 released，含回滚）"}
	cmd.AddCommand(newReleaseNewCmd(), newReleaseListCmd(), newReleaseUpdateCmd())
	return cmd
}

// newReleaseNewCmd 实现 `pulse release new --project K --version v1.0 [--manager] [--no-doc]`。
func newReleaseNewCmd() *cobra.Command {
	var projectKey, version, manager string
	var noDoc bool
	cmd := &cobra.Command{
		Use:   "new",
		Short: "登记一次发版（status 缺省 preparing，负责人归当前操作者；默认建发版文档）",
		Args:  cobra.NoArgs,
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
			vid, err := s.ResolveVersionID(p.ID, version) // 版本必填：ID 整数或项目内版本名
			if err != nil {
				return err
			}
			r := model.Release{ProjectID: p.ID, VersionID: vid}
			managerID, _, err := resolveMemberFlag(s, cmd, "manager", manager)
			if err != nil {
				return err
			}
			if managerID != nil {
				r.ReleaseManagerID = *managerID
			}
			created, err := s.CreateRelease(r, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "发版记录已创建 (id=%d)\n", created.ID)
			if !noDoc { // 默认建发版记录文档（未配置飞书时降级为提示）
				ensureRecordDoc(cmd, s, cfg, "release", created.ID, a.Name)
			}
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&version, "version", "", "发版对应的版本：版本 ID 整数，或项目内版本名（必填，需已创建）")
	cmd.Flags().StringVar(&manager, "manager", "", "发布负责人成员名（已有成员直接使用，不存在则按 human 创建）")
	cmd.Flags().BoolVar(&noDoc, "no-doc", false, "跳过发版文档自动创建（默认 feishu 已配置时按模板建发版文档）")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

// newReleaseListCmd 实现 `pulse release list --project K`。
func newReleaseListCmd() *cobra.Command {
	var projectKey string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目发版记录",
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
			rs, err := s.ListReleases(p.ID)
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
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t版本\t状态\t发布负责人\t发布时间")
			for _, r := range rs {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\t%s\n",
					r.ID, versionNames[r.VersionID], r.Status,
					names[r.ReleaseManagerID], r.ReleasedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	return cmd
}

// newReleaseUpdateCmd 实现 `pulse release update <id> --status preparing|testing|released|rolled_back`；
// 进入 released 的 released_at 由 store 自动补记。
func newReleaseUpdateCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "流转发版状态（进入 released 自动补记发布时间）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("发版 ID 须为整数，收到 %q", args[0])
			}
			if err := checkReleaseStatus(status); err != nil {
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
			r, err := s.UpdateRelease(id, store.ReleaseChanges{Status: &status}, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "发版已更新: status=%s (id=%d)\n", r.Status, r.ID)
			autopushForEntityProject(cmd, s, cfg, r.ProjectID) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "目标状态：preparing|testing|released|rolled_back（必填）")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}
