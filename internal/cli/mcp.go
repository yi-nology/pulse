package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/mcpserver"
	"github.com/zhangyi/pulse/internal/store"
)

// newMCPCmd 实现 `pulse mcp`：以 stdio 传输运行 MCP server，供编码代理接入。
// agent 身份来自 PULSE_ACTOR 环境变量（.mcp.json 的 env 段配置，如 PULSE_ACTOR=codex），
// 为空即拒绝启动。stdio 传输独占 stdout，任何提示只能写 stderr。
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "启动 MCP server（stdio 传输），供编码代理驱动",
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := os.Getenv("PULSE_ACTOR")
			if agentName == "" {
				return errors.New("启动 MCP server 需设置 PULSE_ACTOR")
			}
			s, cfg, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			srv, err := mcpserver.New(s, agentName)
			if err != nil {
				return err
			}
			// 装配点：写工具成功后的自动 push（与 CLI 写命令同一 BestEffort 契约：
			// 未配置/未绑定静默、失败仅 stderr 警告、绝不影响工具结果）。
			// agentName 传入 PULSE_ACTOR：agent 触发的同步活动归因到 agent。
			mcpserver.AutopushFunc = func(st *store.Store, projectKey, agentName string) {
				feishu.BestEffort(st, cfg, projectKey, agentName)
			}
			// 装配点：publish_feishu 桥接到 feishu.PublishReport（保持 mcpserver 零飞书依赖）。
			// 执行者归因到 agent（PULSE_ACTOR，E2E-4）：agent 发起的发布其落款与返回
			// actor 都署 agent 名；沉淀文档前的静默同步等活动同样归属 agent。
			mcpserver.PublishReportFunc = func(st *store.Store, projectKey, report string) (any, error) {
				return publishReportForMCP(st, cfg, projectKey, report, agentName)
			}
			// warnWriter 只在此一次性固定到 stderr：MCP 允许并发执行工具，任何工具回调里
			// 再 SetWarnWriter 都会与另一工具的 autopush 写警告构成数据竞争（接口字无锁）。
			// MCP 模式 stdout 归协议，stderr 本就是警告的唯一合法出口。
			feishu.SetWarnWriter(os.Stderr)
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}

// publishReportForMCP 是 mcpserver.PublishReportFunc 的桥接实现：解析项目与执行者
// 后走 feishu.PublishReport，把文档 token 与发布摘要作为 JSON 结果回给代理。
// agentName 是发起发布的 agent 名（来自 PULSE_ACTOR）：非空时执行者解析为 agent
// 成员（落款/返回 actor 用其名，E2E-4），为空时退回配置的默认人类账号（现状）。
func publishReportForMCP(st *store.Store, cfg *config.Config, projectKey, report, agentName string) (any, error) {
	p, found, err := st.GetProjectByKey(projectKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("项目不存在: %s", projectKey)
	}
	a, _, err := actor.Resolve(st, cfg.DefaultActor, agentName, "")
	if err != nil {
		return nil, err
	}
	c := feishu.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, os.Getenv("PULSE_FEISHU_ENDPOINT"))
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	if err := feishu.PublishReport(ctx, c, st, p, report, a.Name); err != nil {
		return nil, err
	}
	// PublishReport 按值接收项目：自动建档路径补建的 doc token 只在函数内副本上，
	// 从 store 重读保证返回给代理的 doc 非空且与落库一致。
	if fresh, found, err := st.GetProjectByKey(projectKey); err == nil && found {
		p = fresh
	}
	return map[string]any{
		"ok": true, "project": p.Key, "report": report,
		"doc": p.FeishuDocToken, "actor": a.Name,
	}, nil
}
