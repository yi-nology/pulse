package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
)

// reviewKinds / reviewConclusions / concludeConclusions 与 store/schema 约定保持一致。
var reviewKinds = []string{"requirement", "release", "test"}
var reviewConclusions = []string{"pending", "passed", "passed_with_notes", "rejected"}

// concludeConclusions conclude 命令允许的目标结论（pending 是初始占位，不可作为结论回退）。
var concludeConclusions = []string{"passed", "passed_with_notes", "rejected"}

func checkReviewKind(v string) error {
	for _, s := range reviewKinds {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("kind 必须为 %s，收到 %q", strings.Join(reviewKinds, "|"), v)
}

func checkReviewConclusion(v string) error {
	for _, s := range reviewConclusions {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("conclusion 必须为 %s，收到 %q", strings.Join(reviewConclusions, "|"), v)
}

func checkConcludeConclusion(v string) error {
	for _, s := range concludeConclusions {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("conclusion 必须为 %s，收到 %q", strings.Join(concludeConclusions, "|"), v)
}

// newReviewCmd 实现 `pulse review` 子命令组。
func newReviewCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "review", Short: "评审记录（记录评审与结论，纪要内容在飞书文档协作）"}
	cmd.AddCommand(newReviewRecordCmd(), newReviewConcludeCmd())
	return cmd
}

// newReviewRecordCmd 实现 `pulse review record --project K [--requirement ID] --kind ... [--conclusion pending]`。
func newReviewRecordCmd() *cobra.Command {
	var projectKey, kind, conclusion string
	var requirementID int64
	cmd := &cobra.Command{
		Use:   "record",
		Short: "记录一次评审（conclusion 缺省 pending，held_at 缺省当前时刻）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkReviewKind(kind); err != nil {
				return err
			}
			if err := checkReviewConclusion(conclusion); err != nil {
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
			if cmd.Flags().Changed("requirement") && requirementID != 0 {
				if _, found, err := s.GetRequirement(requirementID); err != nil {
					return err
				} else if !found {
					return fmt.Errorf("需求不存在: id=%d", requirementID)
				}
			}
			v, err := s.CreateReview(model.Review{
				ProjectID:     p.ID,
				RequirementID: requirementID,
				Kind:          kind,
				Conclusion:    conclusion,
			}, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "评审已记录: %s (id=%d)\n", v.Kind, v.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	cmd.Flags().StringVar(&kind, "kind", "", "评审类型：requirement|release|test（必填）")
	cmd.Flags().Int64Var(&requirementID, "requirement", 0, "关联需求 ID（可选）")
	cmd.Flags().StringVar(&conclusion, "conclusion", "pending", "评审结论：pending|passed|passed_with_notes|rejected")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

// newReviewConcludeCmd 实现 `pulse review conclude <id> --conclusion passed|passed_with_notes|rejected`。
func newReviewConcludeCmd() *cobra.Command {
	var conclusion string
	cmd := &cobra.Command{
		Use:   "conclude <id>",
		Short: "给出评审结论（pending 为初始占位，不可作为结论）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("评审 ID 须为整数，收到 %q", args[0])
			}
			if err := checkConcludeConclusion(conclusion); err != nil {
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
			v, err := s.UpdateReviewConclusion(id, conclusion, a, behalf) // update 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "评审结论已更新: %s (id=%d)\n", v.Conclusion, v.ID)
			autopushForEntityProject(cmd, s, cfg, v.ProjectID) // 写后自动 push（尽力而为）
			return nil
		},
	}
	cmd.Flags().StringVar(&conclusion, "conclusion", "", "评审结论：passed|passed_with_notes|rejected（必填）")
	_ = cmd.MarkFlagRequired("conclusion")
	return cmd
}
