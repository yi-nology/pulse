// Package actor 把"谁在操作"的两种来源（人类默认账号、代理 agent）解析为成员，
// 并处理 agent 代表人类（delegatedBy）的归属。
package actor

import (
	"errors"
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// Resolve 解析本次操作的执行者：
//   - agentName != "" 时 actor 为 type=agent 成员（get-or-create），
//     并先确保 defaultActor 的 human 成员存在，以便 delegatedBy 能解析到它；
//   - 否则 actor 为 defaultActor（human，get-or-create；defaultActor 为空时报错）；
//   - delegatedBy 非空时解析为 behalf（human，get-or-create，失败即报错）。
func Resolve(s *store.Store, defaultActor, agentName, delegatedBy string) (actor model.Member, behalf *model.Member, err error) {
	if agentName != "" {
		// agent 分支：先确保 defaultActor（human）存在，delegatedBy 才能解析到它
		if defaultActor != "" {
			if _, err := s.GetOrCreateMember(defaultActor, "human"); err != nil {
				return model.Member{}, nil, fmt.Errorf("ensure default actor %q: %w", defaultActor, err)
			}
		}
		actor, err = s.GetOrCreateMember(agentName, "agent")
		if err != nil {
			return model.Member{}, nil, fmt.Errorf("resolve agent %q: %w", agentName, err)
		}
	} else {
		if defaultActor == "" {
			return model.Member{}, nil, errors.New("默认执行者为空：必须提供 defaultActor 或 agentName")
		}
		actor, err = s.GetOrCreateMember(defaultActor, "human")
		if err != nil {
			return model.Member{}, nil, fmt.Errorf("resolve default actor %q: %w", defaultActor, err)
		}
	}
	if delegatedBy != "" {
		m, err := s.GetOrCreateMember(delegatedBy, "human")
		if err != nil {
			return model.Member{}, nil, fmt.Errorf("resolve delegatedBy %q: %w", delegatedBy, err)
		}
		behalf = &m
	}
	return actor, behalf, nil
}
