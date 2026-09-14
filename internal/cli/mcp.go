package cli

import (
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/mcpserver"
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
			s, _, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			srv, err := mcpserver.New(s, agentName)
			if err != nil {
				return err
			}
			// 装配点：Task 13 实现 internal/feishu 后在此注入 mcpserver.PublishReportFunc。
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
