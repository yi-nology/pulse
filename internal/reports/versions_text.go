// versions_text.go 是版本规划报表的文本形态渲染：与 VersionsHTML 共用
// collectVersions 数据源（口径一致），输出 Markdown 子集（`# `/`## ` 标题 +
// `- ` 列表），供 feishu publish 的 md→blocks 转换消费，不复用 HTML 字符串。
package reports

import (
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/store"
)

// VersionsText 生成版本规划报表的文本形态：每版本一个小节（版本名 + 状态 +
// 目标日期标题），小节内列出进度、风险摘要与风险条目 bullets；无法归属到
// 版本的风险汇入"其他风险"小节。无版本时输出占位，不返回空文档。
func VersionsText(s *store.Store, projectID int64, now time.Time) ([]byte, error) {
	p, blocks, otherRisks, err := collectVersions(s, projectID, now)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 版本规划 · %s\n", p.Key)
	if len(blocks) == 0 {
		b.WriteString("## 版本\n- 无\n")
	}
	for _, blk := range blocks {
		target := blk.TargetDate
		if target == "" {
			target = "未排期"
		}
		fmt.Fprintf(&b, "\n## %s（状态 %s，目标 %s）\n", blk.Name, blk.Status, target)
		fmt.Fprintf(&b, "- 进度: %d/%d（%s）\n", blk.Done, blk.Total, blk.DonePctText)
		if blk.OverdueN > 0 {
			fmt.Fprintf(&b, "- 逾期 %d 项\n", blk.OverdueN)
		}
		fmt.Fprintf(&b, "- 风险摘要: %s\n", blk.RiskSummary)
		for _, line := range blk.RiskLines {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if len(otherRisks) > 0 {
		b.WriteString("\n## 其他风险\n")
		for _, line := range otherRisks {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	return []byte(b.String()), nil
}
