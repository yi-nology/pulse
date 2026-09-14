package reports

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// renderHTML 按模板名（templates/ 下的文件名）渲染自包含单文件 HTML。
// 模板内联全部 CSS，不引用任何外部资源与 JS。
func renderHTML(name string, data any) ([]byte, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/"+name)
	if err != nil {
		return nil, fmt.Errorf("reports: parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("reports: execute template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// pctf 输出稳定小数（两位定点），供模板内联 style 使用，避免默认格式抖动。
func pctf(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// numf 输出稳定的一位小数（人日等数值展示）。
func numf(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

// utcToday 返回 now 的 UTC 零点（报表展示层统一口径，见包注释）。
func utcToday(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// parseDate 解析 YYYY-MM-DD 日期；空串或非法格式返回 false（视为未排期）。
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(layoutDate, s)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}
