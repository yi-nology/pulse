// autopush.go 实现"写后自动 push"：CLI 写命令成功后尽力同步一次飞书。
// 设计红线（spec §6 触发策略）：未配置飞书或项目未绑定时静默返回；任何错误只打印
// warning 到 stderr，绝不影响命令退出码——本地写操作的成败与飞书可用性解耦。
package feishu

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/store"
)

// autopushTimeout 限制自动同步的最长耗时：写命令不能被慢网拖住太久。
const autopushTimeout = 30 * time.Second

// BestEffort 在写命令成功后自动 push：未配置凭据、项目不存在或未绑定 base 时静默返回；
// 同步失败仅向 stderr 输出警告（本地变更已保留，恢复后 pulse sync 可补推）。
func BestEffort(s *store.Store, cfg *config.Config, projectKey string) {
	if cfg == nil || cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
		return // 未配置飞书：静默
	}
	p, found, err := s.GetProjectByKey(projectKey)
	if err != nil || !found {
		return // 项目异常按未绑定处理：静默
	}
	if p.FeishuBitableAppToken == "" {
		return // 未绑定：静默（bind 之前一切写命令照旧）
	}
	a, _, err := actor.Resolve(s, cfg.DefaultActor, "", "")
	if err != nil {
		warnAutopush(err)
		return
	}
	c := NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, os.Getenv("PULSE_FEISHU_ENDPOINT"))
	ctx, cancel := context.WithTimeout(context.Background(), autopushTimeout)
	defer cancel()
	if _, err := SyncProject(ctx, c, s, p, a); err != nil {
		warnAutopush(err)
	}
}

// warnAutopush 输出自动同步失败的警告；这是 autopush 唯一的副作用出口
// （经包级 warnWriter，CLI 接线时由 SetWarnWriter 导向当前命令的 stderr）。
func warnAutopush(err error) {
	fmt.Fprintf(warnWriter, "警告: 自动同步飞书失败（本地变更已保留，恢复后执行 pulse sync 可补推）: %v\n", err)
}
