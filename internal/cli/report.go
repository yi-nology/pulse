package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/reports"
	"github.com/zhangyi/pulse/internal/store"
)

// newReportCmd 实现 `pulse report gantt|workload|versions|weekly`。
// 报表只读（不落 activity），默认输出到 stdout，--out 指定文件时写入文件。
func newReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "生成管理报表：甘特图 / 人力负载 / 版本规划 / 周报",
	}
	cmd.AddCommand(
		newReportKindCmd("gantt", "甘特图（版本泳道 + 任务条，依赖以条下文字标注）",
			func(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
				return reports.GanttHTML(s, projectID)
			}),
		newReportKindCmd("workload", "人力负载（成员负载率，人/agent 并列）",
			func(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
				return reports.WorkloadHTML(s, projectID, now)
			}),
		newReportKindCmd("versions", "版本规划（scope/完成度/逾期/风险摘要）",
			func(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
				return reports.VersionsHTML(s, projectID, now)
			}),
		newReportKindCmd("weekly", "周报（Markdown：完成/进行中/风险/未排期/agent 贡献）",
			func(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
				return reports.WeeklyMarkdown(s, projectID, now)
			}),
	)
	return cmd
}

// newReportKindCmd 组装单个报表子命令：--project 必填，--out 缺省 stdout。
// 生成前做 stale 检查（已配置飞书且超过 sync.stale_minutes 未 pull → 先静默同步，
// 失败仅提示数据可能滞后，不影响报表输出）。
func newReportKindCmd(kind, short string, gen func(*store.Store, int64, time.Time) ([]byte, error)) *cobra.Command {
	var projectKey, out string
	cmd := &cobra.Command{
		Use:   kind,
		Short: short,
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
			a, _, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			pullIfStale(s, cfg, p, a, cmd.ErrOrStderr())
			data, err := gen(s, p.ID, time.Now())
			if err != nil {
				return err
			}
			if out == "" {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return fmt.Errorf("写入报表 %s: %w", out, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "报表已写入: %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&out, "out", "", "输出文件路径（缺省打印到 stdout）")
	return cmd
}
