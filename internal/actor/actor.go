package actor

import (
	"errors"
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// Resolve 解析本次操作的执行者。两个执行者槽位（defaultActor / agentName）的
// 成员类型**以库里既有成员为准**（GetOrCreateMemberAs 宽松档）：同名成员已存在
// 时直接取回——执行者/指派人的名字不该因为调用方槽位缺省 human/agent 而撞
// 类型冲突（issue #2：agent 成员名进 human 槽位曾在此必报冲突）；不存在才按
// 槽位缺省类型创建（defaultActor→human、agentName→agent）。
//   - agentName != "" 时 actor 为 agentName 槽位成员，并先确保 defaultActor
//     成员存在，以便 delegatedBy 能解析到它；
//   - 否则 actor 为 defaultActor 槽位成员（defaultActor 为空时报错）；
//   - delegatedBy 非空时解析为 behalf（human，get-or-create，失败即报错）——
//     「代表谁执行」契约上就是人类成员，保持严格档。
func Resolve(s *store.Store, defaultActor, agentName, delegatedBy string) (actor model.Member, behalf *model.Member, err error) {
	if agentName != "" {
		// agent 分支：先确保 defaultActor 存在，delegatedBy 才能解析到它
		if defaultActor != "" {
			if _, err := s.GetOrCreateMemberAs(defaultActor, "human"); err != nil {
				return model.Member{}, nil, fmt.Errorf("ensure default actor %q: %w", defaultActor, err)
			}
		}
		actor, err = s.GetOrCreateMemberAs(agentName, "agent")
		if err != nil {
			return model.Member{}, nil, fmt.Errorf("resolve agent %q: %w", agentName, err)
		}
	} else {
		if defaultActor == "" {
			return model.Member{}, nil, errors.New("默认执行者为空：必须提供 defaultActor 或 agentName")
		}
		actor, err = s.GetOrCreateMemberAs(defaultActor, "human")
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
