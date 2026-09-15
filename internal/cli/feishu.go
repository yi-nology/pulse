package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/model"
)

// newFeishuCmd 实现 `pulse feishu` 子命令组（bind / publish）。
func newFeishuCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "feishu", Short: "飞书集成（bind 创建/绑定同步 base 与沉淀文档，publish 沉淀报表）"}
	cmd.AddCommand(newFeishuBindCmd(), newFeishuPublishCmd())
	return cmd
}

// newFeishuBindCmd 实现 `pulse feishu bind --project demo [--app-token X --task-table Y --version-table Z --doc W]`。
//
// 两种模式：
//   - 创建模式（默认）：经 feishu.Bind 新建 base（任务表+版本表+甘特视图+六实体表）与
//     沉淀文档并写回 token；幂等——项目已有 bitable_app_token 时直接提示已绑定，
//     不产生任何调用。
//   - 采用模式（给 --app-token）：不创建任何飞书资源，仅把既有 base/表/文档 token 写回项目，
//     供双机共享同一 base（Task 14 E2E 场景；--task-table/--version-table 必填，--doc 可省，
//     省略时 publish 会自动补建文档）。六实体表 id 可经
//     --requirements-table/--reviews-table/--meetings-table/--bugs-table/
//     --submissions-table/--releases-table 传入（均可省），创建方 bind 输出的共享提示
//     含全部 id，整行复制即可；未传的表保留本机既有值，六实体未配置时 sync 自动跳过。
//
// PULSE_FEISHU_ENDPOINT 环境变量可覆盖官方域名（测试注入 httptest 地址用）。
func newFeishuBindCmd() *cobra.Command {
	var projectKey, appToken, taskTable, versionTable, docToken string
	var requirementsTable, reviewsTable, meetingsTable, bugsTable, submissionsTable, releasesTable string
	cmd := &cobra.Command{
		Use:   "bind",
		Short: "为项目创建（或采用既有）飞书同步 base 与沉淀文档，token 写回项目",
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
			out := cmd.OutOrStdout()
			adopting := appToken != ""

			// 幂等：已绑定且未显式要求采用/改绑 → 原样提示，零调用零写库（也不落活动）。
			if p.FeishuBitableAppToken != "" && !adopting {
				fmt.Fprintf(out, "项目 %s 已绑定飞书，无需重复绑定（幂等跳过）\n", p.Key)
				printBoundTokens(out, p, false)
				return nil
			}
			// 创建模式需要飞书凭据（采用模式的 token 全部来自命令行，但后续 sync 同样依赖凭据，故统一要求）。
			if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
				return errors.New("未配置飞书：请先在 $PULSE_HOME/config.yaml 配置 feishu.app_id/app_secret" +
					"（或设置环境变量 PULSE_FEISHU_APP_SECRET），再执行 pulse feishu bind 绑定项目")
			}
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}

			if adopting { // 采用既有 base：零 API 调用，仅写回 token
				if taskTable == "" || versionTable == "" {
					return errors.New("采用既有 base 时必须同时提供 --task-table 与 --version-table" +
						"（可在创建方 bind 输出中复制）")
				}
				p.FeishuBitableAppToken = appToken
				p.FeishuTaskTableID = taskTable
				p.FeishuVersionTableID = versionTable
				if docToken != "" {
					p.FeishuDocToken = docToken
				}
				if err := s.SaveProject(p); err != nil {
					return err
				}
				// 六实体表 id（均可省）：合并写回 feishu_tables_json——指定的表覆盖，
				// 未指定的表保留既有值（GetFeishuTables 对空 JSON 返回零值，即新建语义）
				if requirementsTable != "" || reviewsTable != "" || meetingsTable != "" ||
					bugsTable != "" || submissionsTable != "" || releasesTable != "" {
					tables, err := s.GetFeishuTables(p.ID)
					if err != nil {
						return err
					}
					if requirementsTable != "" {
						tables.Requirements = requirementsTable
					}
					if reviewsTable != "" {
						tables.Reviews = reviewsTable
					}
					if meetingsTable != "" {
						tables.Meetings = meetingsTable
					}
					if bugsTable != "" {
						tables.Bugs = bugsTable
					}
					if submissionsTable != "" {
						tables.TestSubmissions = submissionsTable
					}
					if releasesTable != "" {
						tables.Releases = releasesTable
					}
					if err := s.SaveFeishuTables(p.ID, tables); err != nil {
						return err
					}
				}
				if err := logAction(s, a, behalf, p.ID, "feishu_bind", "project", p.ID); err != nil {
					return err
				}
				fmt.Fprintf(out, "项目 %s 已绑定既有飞书 base:\n", p.Key)
				printBoundTokens(out, p, docToken == "")
				return nil
			}

			c := feishu.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, os.Getenv("PULSE_FEISHU_ENDPOINT"))
			p, err = feishu.Bind(cmd.Context(), c, s, p)
			if err != nil {
				return err
			}
			if err := logAction(s, a, behalf, p.ID, "feishu_bind", "project", p.ID); err != nil {
				return err
			}
			fmt.Fprintf(out, "项目 %s 已绑定飞书:\n", p.Key)
			printBoundTokens(out, p, false)
			// 共享提示含全部表 id（含六实体表），机器 B 可整行复制到采用模式 bind
			tables, err := s.GetFeishuTables(p.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "其他机器共享提示: 执行 pulse feishu bind --project %s --app-token %s"+
				" --task-table %s --version-table %s --doc %s"+
				" --requirements-table %s --reviews-table %s --meetings-table %s"+
				" --bugs-table %s --submissions-table %s --releases-table %s 可绑定同一 base\n",
				p.Key, p.FeishuBitableAppToken, p.FeishuTaskTableID, p.FeishuVersionTableID, p.FeishuDocToken,
				tables.Requirements, tables.Reviews, tables.Meetings,
				tables.Bugs, tables.TestSubmissions, tables.Releases)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&appToken, "app-token", "", "采用既有 base 的 app_token（给出即进入采用模式，不创建任何资源）")
	cmd.Flags().StringVar(&taskTable, "task-table", "", "既有任务表 table_id（采用模式必填）")
	cmd.Flags().StringVar(&versionTable, "version-table", "", "既有版本表 table_id（采用模式必填）")
	cmd.Flags().StringVar(&docToken, "doc", "", "既有沉淀文档 document_id（采用模式可选，缺省时 publish 会自动补建）")
	cmd.Flags().StringVar(&requirementsTable, "requirements-table", "", "既有需求表 table_id（采用模式可选，六实体同步用）")
	cmd.Flags().StringVar(&reviewsTable, "reviews-table", "", "既有评审表 table_id（采用模式可选，六实体同步用）")
	cmd.Flags().StringVar(&meetingsTable, "meetings-table", "", "既有会议表 table_id（采用模式可选，六实体同步用）")
	cmd.Flags().StringVar(&bugsTable, "bugs-table", "", "既有 bug 表 table_id（采用模式可选，六实体同步用）")
	cmd.Flags().StringVar(&submissionsTable, "submissions-table", "", "既有提测表 table_id（采用模式可选，六实体同步用）")
	cmd.Flags().StringVar(&releasesTable, "releases-table", "", "既有发版表 table_id（采用模式可选，六实体同步用）")
	return cmd
}

// printBoundTokens 打印项目当前绑定的飞书 token；missingDoc 为 true 时提示文档未绑定。
func printBoundTokens(out io.Writer, p model.Project, missingDoc bool) {
	fmt.Fprintf(out, "  base(app_token):  %s\n", p.FeishuBitableAppToken)
	fmt.Fprintf(out, "  任务表(table_id): %s\n", p.FeishuTaskTableID)
	fmt.Fprintf(out, "  版本表(table_id): %s\n", p.FeishuVersionTableID)
	fmt.Fprintf(out, "  沉淀文档(doc):    %s\n", p.FeishuDocToken)
	if missingDoc {
		fmt.Fprintln(out, "  提示: 未绑定文档，publish 时会自动创建并写回")
	}
}

// newFeishuPublishCmd 实现 `pulse feishu publish --project demo --report weekly|versions|all`：
// 先静默同步（失败仅警告，离线可发布本地数据），生成报表并追加到绑定文档；
// 未绑定文档时自动创建并写回。每次 publish 追加新块（时间线性质，重跑产生新段落
// 是文档化行为），顶部防混淆标题块 + 底部落款块标识来源与触发人。
func newFeishuPublishCmd() *cobra.Command {
	var projectKey, report string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "把报表（weekly|versions|all）沉淀到项目绑定的飞书文档",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch report {
			case "weekly", "versions", "all":
			default:
				return fmt.Errorf("report 必须为 weekly|versions|all，收到 %q", report)
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
				return errors.New("未配置飞书：请先在 $PULSE_HOME/config.yaml 配置 feishu.app_id/app_secret" +
					"（或设置环境变量 PULSE_FEISHU_APP_SECRET），再执行 pulse feishu bind 绑定项目")
			}
			p, err := requireProjectFlag(s, projectKey)
			if err != nil {
				return err
			}
			a, behalf, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			feishu.SetWarnWriter(cmd.ErrOrStderr()) // 同步/发布的警告导向当前命令 stderr
			c := feishu.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, os.Getenv("PULSE_FEISHU_ENDPOINT"))
			ctx, cancel := context.WithTimeout(cmd.Context(), syncTimeout)
			defer cancel()
			if err := feishu.PublishReport(ctx, c, s, p, report, a.Name); err != nil {
				return err
			}
			// PublishReport 按值接收项目：自动建档路径补建的 doc token 只在函数内
			// 副本上，输出前必须从 store 重读，否则未绑文档的首次 publish 打印空 token。
			if fresh, found, err := s.GetProjectByKey(p.Key); err == nil && found {
				p = fresh
			}
			if err := logAction(s, a, behalf, p.ID, "feishu_publish", "project", p.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "报表已沉淀到飞书文档: %s（report: %s，触发人: %s）\n",
				p.FeishuDocToken, report, a.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	cmd.Flags().StringVar(&report, "report", "", "报表类型：weekly | versions | all（必填）")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("report")
	return cmd
}
