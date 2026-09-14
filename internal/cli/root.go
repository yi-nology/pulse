package cli

import (
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	return &cobra.Command{Use: "pulse", Short: "本地优先的项目管理核心"}
}

// Execute 是 main 的唯一入口。
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
