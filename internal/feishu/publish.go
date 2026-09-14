// publish.go 实现报表沉淀到飞书文档（docx）：先静默 SyncProject 保证数据新鲜
// （失败仅警告——离线可发布本地数据），再生成 weekly（Markdown）或 versions
// （结构化文本，经 reports.VersionsText）内容，经简易 md→blocks 转换追加为
// 文档块。未绑定文档时自动 DocCreate 并 SaveProject 写回项目。
//
// 幂等性说明（文档化行为）：每次 publish 都在文档末尾追加新块（报表是时间线
// 性质，不去重、重跑产生新段落）；顶部写入形如 "周报 2026-W38（生成于
// 2026-09-15 08:00，由 codex 触发）" 的 heading2 防混淆块，底部写入
// "由 pulse 导出 · 触发人 {actor}" 落款块。
package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/actor"
	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/reports"
	"github.com/zhangyi/pulse/internal/store"
)

// docx 块类型编号（飞书开放平台「块数据结构」block_type 枚举，已按官方文档核对）。
const (
	blockTypeText     = 2  // 文本块
	blockTypeHeading2 = 4  // 二级标题块
	blockTypeBullet   = 12 // 无序列表块
	blockTypeTodo     = 17 // 待办块
)

// PublishReport 把项目的指定报表（weekly|versions|all）沉淀到飞书文档：
// 未绑定文档自动创建并写回；all = weekly + versions 两次独立追加。
// actorName 是触发人展示名（防混淆标题与落款、活动归属共用）。
func PublishReport(ctx context.Context, c *Client, s *store.Store, p model.Project,
	report string, actorName string) error {
	switch report {
	case "weekly", "versions", "all":
	default:
		return fmt.Errorf("report 必须为 weekly|versions|all，收到 %q", report)
	}

	// 静默同步（仅已绑定 base 时）：失败只警告，基于本地数据继续发布。
	if p.FeishuBitableAppToken != "" {
		if a, _, err := actor.Resolve(s, actorName, "", ""); err == nil {
			if _, err := SyncProject(ctx, c, s, p, a); err != nil {
				fmt.Fprintf(warnWriter, "警告: 同步失败，将基于本地数据发布（恢复后执行 pulse sync 可补推）: %v\n", err)
			}
		}
	}

	// 未绑定文档：自动补建并写回项目（采用模式允许 --doc 省略的补齐路径）。
	if p.FeishuDocToken == "" {
		docToken, err := c.API().DocCreate(ctx, "", p.Name+" 沉淀文档")
		if err != nil {
			return fmt.Errorf("创建沉淀文档失败: %w", err)
		}
		p.FeishuDocToken = docToken
		if err := s.SaveProject(p); err != nil {
			return fmt.Errorf("写回沉淀文档 token 失败: %w", err)
		}
	}

	now := time.Now()
	var kinds []string
	switch report {
	case "weekly":
		kinds = []string{"weekly"}
	case "versions":
		kinds = []string{"versions"}
	case "all":
		kinds = []string{"weekly", "versions"}
	}
	for _, kind := range kinds {
		blocks, err := publishBlocks(s, p.ID, kind, now, actorName)
		if err != nil {
			return err
		}
		if err := c.API().BlockAppend(ctx, p.FeishuDocToken, blocks); err != nil {
			return fmt.Errorf("追加报表块失败（%s）: %w", kind, err)
		}
	}
	return nil
}

// publishBlocks 生成一次 publish 的完整块序列：防混淆标题 → 报表正文 → 落款。
func publishBlocks(s *store.Store, projectID int64, kind string, now time.Time, actorName string) ([]map[string]any, error) {
	var (
		content []byte
		header  string
		err     error
	)
	switch kind {
	case "weekly":
		content, err = reports.WeeklyMarkdown(s, projectID, now)
		y, w := now.UTC().ISOWeek()
		header = fmt.Sprintf("周报 %d-W%02d（生成于 %s，由 %s 触发）",
			y, w, now.Format("2006-01-02 15:04"), actorName)
	case "versions":
		content, err = reports.VersionsText(s, projectID, now)
		header = fmt.Sprintf("版本规划（生成于 %s，由 %s 触发）",
			now.Format("2006-01-02 15:04"), actorName)
	default:
		return nil, fmt.Errorf("report 必须为 weekly|versions|all，收到 %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("生成 %s 报表失败: %w", kind, err)
	}
	blocks := append([]map[string]any{headingBlock(header)}, markdownToBlocks(string(content))...)
	return append(blocks, textBlock(fmt.Sprintf("由 pulse 导出 · 触发人 %s", actorName))), nil
}

// markdownToBlocks 把报表的 Markdown 子集逐行转为 docx 块：`## `/`# `→heading2
// （h1 降级为二级标题）、`- [ ] `→todo、`- `→bullet、其余非空行→text；空行丢弃。
// 约 30 行的简易实现（计划决策：不引第三方 md 库）。
func markdownToBlocks(md string) []map[string]any {
	var blocks []map[string]any
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "## "):
			blocks = append(blocks, headingBlock(strings.TrimSpace(line[3:])))
		case strings.HasPrefix(line, "# "):
			blocks = append(blocks, headingBlock(strings.TrimSpace(line[2:])))
		case strings.HasPrefix(line, "- [ ] "):
			blocks = append(blocks, todoBlock(strings.TrimSpace(line[6:])))
		case strings.HasPrefix(line, "- "):
			blocks = append(blocks, bulletBlock(strings.TrimSpace(line[2:])))
		default:
			blocks = append(blocks, textBlock(line))
		}
	}
	return blocks
}

// blockOf 组装单文本元素块：{"block_type":N,"<key>":{"elements":[{"text_run":{"content":...}}]}}。
// 标题含 <>& 等字符时无需 HTML 转义——BlockAppend 走 encoding/json 序列化天然安全。
func blockOf(blockType int, key, content string) map[string]any {
	return map[string]any{
		"block_type": blockType,
		key: map[string]any{
			"elements": []map[string]any{
				{"text_run": map[string]any{"content": content}},
			},
		},
	}
}

func headingBlock(content string) map[string]any {
	return blockOf(blockTypeHeading2, "heading2", content)
}
func textBlock(content string) map[string]any   { return blockOf(blockTypeText, "text", content) }
func bulletBlock(content string) map[string]any { return blockOf(blockTypeBullet, "bullet", content) }
func todoBlock(content string) map[string]any   { return blockOf(blockTypeTodo, "todo", content) }
