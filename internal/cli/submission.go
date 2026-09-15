package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// submissionUpdateStatuses submit update 允许的目标状态（draft 仅创建缺省，不作为更新目标）。
var submissionUpdateStatuses = []string{"submitted", "testing", "passed", "failed"}

func checkSubmissionUpdateStatus(v string) error {
	for _, s := range submissionUpdateStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("status 必须为 %s，收到 %q", strings.Join(submissionUpdateStatuses, "|"), v)
}

// newSubmissionCmd 实现 `pulse submit` 子命令组。
func newSubmissionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "submit", Short: "提测单管理（从 draft 提交到测试结论的流转）"}
	cmd.AddCommand(newSubmitCreateCmd(), newSubmitListCmd(), newSubmitUpdateCmd())
	return cmd
}

// newSubmitCreateCmd 实现 `pulse submit create --project K --version v1.0 [--requirement] [--test-owner]`。
func newSubmitCreateCmd() *cobra.Command {
	var projectKey, version, requirement, testOwner string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建提测单（status 缺省 draft，提测人归当前操作者）",
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
			t := model.TestSubmission{ProjectID: p.ID, VersionID: vid}
			if requirement != "" {
				rid, err := strconv.ParseInt(requirement, 10, 64)
				if err != nil {
					return fmt.Errorf("需求 ID 须为整数，收到 %q", requirement)
				}
				if _, found, err := s.GetRequirement(rid); err != nil {
					return err
				} else if !found {
					return fmt.Errorf("需求不存在: id=%d", rid)
				}
				t.RequirementID = rid
			}
			testOwnerID, _, err := resolveMemberFlag(s, cmd, "test-owner", testOwner)
			if err != nil {
				return err
			}
			if testOwnerID != nil {
				t.TestOwnerID = *testOwnerID
			}
			created, err := s.CreateTestSubmission(t, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "提测单已创建 (id=%d)\n", created.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&version, "version", "", "提测版本：版本 ID 整数，或项目内版本名（必填，需已创建）")
	cmd.Flags().StringVar(&requirement, "requirement", "", "关联需求 ID（须已存在）")
	cmd.Flags().StringVar(&testOwner, "test-owner", "", "测试负责人成员名（已有成员直接使用，不存在则按 human 创建）")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

// newSubmitListCmd 实现 `pulse submit list --project K [--version]`。
func newSubmitListCmd() *cobra.Command {
	var projectKey, version string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目提测单（--version 按版本过滤）",
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
			var versionID int64
			if version != "" {
				if versionID, err = s.ResolveVersionID(p.ID, version); err != nil {
					return err
				}
			}
			ts, err := s.ListTestSubmissions(p.ID, versionID)
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
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t版本\t状态\t提测人\t测试负责人\t提交时间")
			for _, t := range ts {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\t%s\t%s\n",
					t.ID, versionNames[t.VersionID], t.Status,
					names[t.SubmittedBy], names[t.TestOwnerID], t.SubmittedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&version, "version", "", "按版本过滤：版本 ID 整数，或项目内版本名")
	return cmd
}

// newSubmitUpdateCmd 实现 `pulse submit update <id> --status submitted|testing|passed|failed`；
// submitted_at/concluded_at 由 store 依状态流转自动补记。
func newSubmitUpdateCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "流转提测单状态（提交/测试时间由 store 自动补记）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("提测单 ID 须为整数，收到 %q", args[0])
			}
			if err := checkSubmissionUpdateStatus(status); err != nil {
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
			t, err := s.UpdateTestSubmission(id, store.SubmissionChanges{Status: &status}, a, behalf)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "提测单已更新: status=%s (id=%d)\n", t.Status, t.ID)
			autopushForEntityProject(cmd, s, cfg, t.ProjectID) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "目标状态：submitted|testing|passed|failed（必填）")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}
