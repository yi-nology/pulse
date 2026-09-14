package cli

import (
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

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
			mcpserver.AutopushFunc = func(st *store.Store, projectKey string) {
				feishu.BestEffort(st, cfg, projectKey)
			}
			// Task 13 实现 internal/feishu 的 PublishReport 后在此注入 mcpserver.PublishReportFunc。
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
