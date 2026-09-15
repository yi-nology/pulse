package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
)

// newMeetingCmd 实现 `pulse meeting` 子命令组。
func newMeetingCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "meeting", Short: "会议记录（登记纪要入口，内容在飞书文档协作维护）"}
	cmd.AddCommand(newMeetingRecordCmd(), newMeetingListCmd())
	return cmd
}

// newMeetingRecordCmd 实现 `pulse meeting record <title> --project K`。
func newMeetingRecordCmd() *cobra.Command {
	var projectKey string
	cmd := &cobra.Command{
		Use:   "record <title>",
		Short: "登记一次会议（held_at 缺省当前时刻，纪要在飞书文档协作）",
		Args:  cobra.ExactArgs(1),
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
			m, err := s.CreateMeeting(model.Meeting{
				ProjectID: p.ID, Title: args[0],
			}, a, behalf) // create 活动由 store 落库
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "会议已记录: %s (id=%d)\n", m.Title, m.ID)
			bestEffort(cmd, s, cfg, p.Key) // 写后自动 push（尽力而为，失败不影响退出码）
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "所属项目 key（必填）")
	return cmd
}

// newMeetingListCmd 实现 `pulse meeting list --project K`。
func newMeetingListCmd() *cobra.Command {
	var projectKey string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出项目会议记录",
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
			ms, err := s.ListMeetings(p.ID)
			if err != nil {
				return err
			}
			names, err := memberNames(s)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\t标题\t时间\t创建人")
			for _, m := range ms {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\n",
					m.ID, m.Title, m.HeldAt, names[m.CreatedBy])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectKey, "project", "", "项目 key（必填）")
	return cmd
}
