// sync.go 组装 `pulse sync` 命令与两处同步触发策略（spec §6）：
//   - `pulse sync`：显式双向同步（先 push 本地变更，再 pull 共享 Bitable 变更）；
//   - 报表命令前 stale 检查：已配置飞书且项目已绑定时，last_pull 超过
//     sync.stale_minutes（默认 10 分钟）即先静默同步一次，失败仅提示"数据可能滞后"；
//   - 写命令后自动 push：见 internal/feishu/autopush.go 的 BestEffort（此处提供
//     只有任务 ID 的命令所需的按 ID 反查项目接线）。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// syncTimeout 限制一次同步的最长耗时（CLI 命令与 stale 检查共用）。
const syncTimeout = 30 * time.Second

// newSyncCmd 实现 `pulse sync --project demo`。
func newSyncCmd() *cobra.Command {
	var projectKey string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "双向同步：先推送本地变更，再拉取共享 Bitable 变更",
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
			if err := requireFeishuReady(cfg, p); err != nil {
				return err
			}
			a, _, err := resolveActor(s, cfg, cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), syncTimeout)
			defer cancel()
			res, err := feishu.SyncProject(ctx, newSyncClient(cfg), s, p, a)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "同步完成: 已推送 %d、拉取 %d、跳过回声 %d、废弃 %d、冲突 %d\n",
				res.Pushed, res.Pulled, res.SkippedEcho, res.Deprecated, len(res.Conflicts))
			for _, c := range res.Conflicts {
				fmt.Fprintf(out, "冲突: %s\n", c)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(out, "警告: %s\n", w)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

// requireFeishuReady 对未配置凭据或未绑定 base 的项目给出引导性中文报错
// （spec：未配置飞书时 sync 给出引导性报错，本地功能完全不受影响）。
func requireFeishuReady(cfg *config.Config, p model.Project) error {
	if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
		return errors.New("未配置飞书：请先在 $PULSE_HOME/config.yaml 配置 feishu.app_id/app_secret" +
			"（或设置环境变量 PULSE_FEISHU_APP_SECRET），再执行 pulse feishu bind 绑定项目")
	}
	if p.FeishuBitableAppToken == "" {
		return fmt.Errorf("项目 %s 未绑定飞书：请先执行 pulse feishu bind --project %s", p.Key, p.Key)
	}
	return nil
}

// newSyncClient 构建飞书客户端；PULSE_FEISHU_ENDPOINT 可重定向（测试注入 httptest 用）。
func newSyncClient(cfg *config.Config) *feishu.Client {
	return feishu.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, os.Getenv("PULSE_FEISHU_ENDPOINT"))
}

// pullIfStale 是报表命令前的 stale 检查：已配置飞书且项目已绑定、且 last_pull 超过
// sync.stale_minutes 时先静默同步一次；同步失败只向 errOut 提示数据可能滞后，
// 报表照常基于本地数据生成。未配置/未绑定时零动作、零提示（纯本地报表路径）。
func pullIfStale(s *store.Store, cfg *config.Config, p model.Project, a model.Member, errOut io.Writer) {
	if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" || p.FeishuBitableAppToken == "" {
		return
	}
	stale := true
	if raw, found, err := feishu.GetSyncState(s, feishu.LastPullKey(p.ID)); err == nil && found {
		if ts, err := strconv.ParseInt(raw, 10, 64); err == nil &&
			time.Now().Unix()-ts < int64(cfg.Sync.StaleMinutes)*60 {
			stale = false
		}
	}
	if !stale {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	if _, err := feishu.SyncProject(ctx, newSyncClient(cfg), s, p, a); err != nil {
		fmt.Fprintf(errOut, "警告: 同步失败，报表数据可能滞后: %v\n", err)
	}
}

// bestEffort 把自动同步的警告导向当前命令的 stderr 后执行写后自动 push
// （尽力而为：任何失败都不影响命令退出码）。
func bestEffort(cmd *cobra.Command, s *store.Store, cfg *config.Config, projectKey string) {
	feishu.SetWarnWriter(cmd.ErrOrStderr())
	feishu.BestEffort(s, cfg, projectKey)
}

// ensureRecordDoc 供六实体 create/record 命令补建协作记录文档（spec §3.2：默认
// create 即建文档，--no-doc 跳过）。未配置飞书或创建失败时仅向 stderr 提示
// "已保存记录（无文档）"——记录已落库，文档创建尽力而为，不影响命令退出码
// （与 bestEffort 同哲学）。
func ensureRecordDoc(cmd *cobra.Command, s *store.Store, cfg *config.Config, entityKind string, id int64, actorName string) {
	var c *feishu.Client
	if cfg.Feishu.AppID != "" && cfg.Feishu.AppSecret != "" {
		c = newSyncClient(cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	token, err := feishu.EnsureRecordDoc(ctx, c, s, entityKind, id, actorName)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "提示: 已保存记录（无文档）: %v\n", err)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "协作文档已创建: %s\n", token)
}

// autopushForTask 供只有任务 ID、没有 --project 旗标的写命令（task update/rm/dep）
// 接线：按任务反查所属项目后执行写后自动 push。
func autopushForTask(cmd *cobra.Command, s *store.Store, cfg *config.Config, taskID int64) {
	t, found, err := s.GetTask(taskID)
	if err != nil || !found {
		return
	}
	if p, found := projectByID(s, t.ProjectID); found {
		bestEffort(cmd, s, cfg, p.Key)
	}
}

// autopushForVersion 供没有 --project 旗标的 version update 接线：按版本反查项目。
func autopushForVersion(cmd *cobra.Command, s *store.Store, cfg *config.Config, versionID int64) {
	v, found, err := s.GetVersion(versionID)
	if err != nil || !found {
		return
	}
	if p, found := projectByID(s, v.ProjectID); found {
		bestEffort(cmd, s, cfg, p.Key)
	}
}

// autopushForEntityProject 供六实体（需求/评审/会议/bug/提测/发版）中没有 --project
// 旗标的写命令（update/conclude）接线：写路径已返回实体，按其 ProjectID 反查项目。
func autopushForEntityProject(cmd *cobra.Command, s *store.Store, cfg *config.Config, projectID int64) {
	if p, found := projectByID(s, projectID); found {
		bestEffort(cmd, s, cfg, p.Key)
	}
}

// projectByID 按 ID 找项目（项目数很小，线性扫描即可）。
func projectByID(s *store.Store, id int64) (model.Project, bool) {
	ps, err := s.ListProjects()
	if err != nil {
		return model.Project{}, false
	}
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return model.Project{}, false
}
