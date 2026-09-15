package feishu

import "testing"

// 真实 Bitable 文本字段读回形态是富文本数组 [{type:"text",text:"..."}]（2026-09-15 真实租户实测），
// 归一化后映射层才能按裸值做回声 hash 与解析。
func TestToTextFlattensRichTextSegments(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"单段富文本", []any{map[string]any{"type": "text", "text": "v0.9"}}, "v0.9"},
		{"多段拼接", []any{map[string]any{"text": "登录"}, map[string]any{"text": "接口"}}, "登录接口"},
		{"数字段", []any{map[string]any{"text": float64(1), "type": "text"}}, "1"},
		{"裸字符串不受影响", "todo", "todo"},
		{"数字不受影响", float64(3), "3"},
	}
	for _, c := range cases {
		if got := toText(c.in); got != c.want {
			t.Errorf("%s: toText=%q want %q", c.name, got, c.want)
		}
	}
}

func TestToFloatFlattensRichTextSegments(t *testing.T) {
	if got := toFloat([]any{map[string]any{"text": float64(2), "type": "text"}}); got != 2 {
		t.Errorf("toFloat=%v want 2", got)
	}
	if got := toFloat([]any{map[string]any{"text": "1.5", "type": "text"}}); got != 1.5 {
		t.Errorf("toFloat=%v want 1.5", got)
	}
}
