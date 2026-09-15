package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// newRootCmd 组装命令树；输出经 SetOut/SetErr 注入，测试用 SetArgs 驱动。
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "pulse",
		Short:        "本地优先的项目管理核心",
		SilenceUsage: true, // RunE 出错时只报错误，不刷 usage
	}
	root.PersistentFlags().String("agent", "", "以指定 agent 成员身份执行")
	root.PersistentFlags().String("delegated-by", "", "agent 代理执行时所代表的人类成员")
	root.AddCommand(
		newInitCmd(),
		newProjectCmd(),
		newMemberCmd(),
		newTaskCmd(),
		newVersionCmd(),
		newRequirementCmd(),
		newReviewCmd(),
		newMeetingCmd(),
		newBugCmd(),
		newSubmissionCmd(),
		newReleaseCmd(),
		newSyncCmd(),
		newReportCmd(),
		newFeishuCmd(),
		newMCPCmd(),
	)
	return root
}

// Execute 是 main 的唯一入口；命令出错即退出码 1。
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// openApp 按配置路径（PULSE_HOME 可重定向）加载配置并打开数据库；调用方负责 Close。
func openApp() (*store.Store, *config.Config, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(config.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return s, cfg, nil
}

// resolveActor 依据全局 --agent / --delegated-by 标志解析本次操作执行者（缺省用 config.DefaultActor）。
func resolveActor(s *store.Store, cfg *config.Config, cmd *cobra.Command) (model.Member, *model.Member, error) {
	agent, _ := cmd.Flags().GetString("agent")
	delegatedBy, _ := cmd.Flags().GetString("delegated-by")
	return actor.Resolve(s, cfg.DefaultActor, agent, delegatedBy)
}
