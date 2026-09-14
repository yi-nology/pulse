package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// logAction 记录一条 CLI 动作活动；projectID/entityID 为 0 表示"无归属"（落 NULL）。
func logAction(s *store.Store, actor model.Member, behalf *model.Member, projectID int64,
	action, entityType string, entityID int64) error {
	var onBehalfOf int64
	if behalf != nil {
		onBehalfOf = behalf.ID
	}
	return s.LogActivity(model.Activity{
		ProjectID: projectID, ActorID: actor.ID, ActorType: actor.Type,
		OnBehalfOf: onBehalfOf, Action: action, EntityType: entityType,
		EntityID: entityID, Detail: "{}",
	})
}

// newInitCmd 实现 `pulse init <key>`：创建项目并落活动。
func newInitCmd() *cobra.Command {
	var name, desc string
	cmd := &cobra.Command{
		Use:   "init <key>",
		Short: "创建项目",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if name == "" {
				name = key
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
			p, err := s.CreateProject(key, name, desc)
			if err != nil {
				if errors.Is(err, store.ErrDuplicateProject) {
					return fmt.Errorf("项目已存在: %s", key)
				}
				return err
			}
			if err := logAction(s, a, behalf, p.ID, "create", "project", p.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "项目已创建: %s (id=%d)\n", key, p.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "项目名称（缺省同 key）")
	cmd.Flags().StringVar(&desc, "desc", "", "项目描述")
	return cmd
}

// newProjectCmd 实现 `pulse project` 子命令组。
func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "项目查看"}
	cmd.AddCommand(newProjectListCmd())
	return cmd
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出全部项目",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			ps, err := s.ListProjects()
			if err != nil {
				return err
			}
			for _, p := range ps {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\n", p.ID, p.Key, p.Name, p.Status)
			}
			return nil
		},
	}
}

// newMemberCmd 实现 `pulse member` 子命令组。
func newMemberCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "member", Short: "成员管理"}
	cmd.AddCommand(newMemberAddCmd(), newMemberListCmd())
	return cmd
}

func newMemberAddCmd() *cobra.Command {
	var typ string
	var capacity float64
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "添加成员",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if typ != "human" && typ != "agent" {
				return fmt.Errorf("type 必须为 human 或 agent，收到 %q", typ)
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
			m, err := s.GetOrCreateMember(name, typ)
			if err != nil {
				return err
			}
			if err := s.SetMemberCapacity(m.ID, capacity); err != nil {
				return err
			}
			m.Capacity = capacity
			if err := logAction(s, a, behalf, 0, "create", "member", m.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "成员已添加: %s (id=%d)\n", m.Name, m.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "human", "成员类型：human | agent")
	cmd.Flags().Float64Var(&capacity, "capacity", 5, "每周可投入人日")
	return cmd
}

func newMemberListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出全部成员",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openApp()
			if err != nil {
				return err
			}
			defer s.Close()
			ms, err := s.ListMembers()
			if err != nil {
				return err
			}
			for _, m := range ms {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%g\n", m.ID, m.Name, m.Type, m.Capacity)
			}
			return nil
		},
	}
}
