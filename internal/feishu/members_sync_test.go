package feishu

import (
	"context"
	"testing"
)

// memberFields 从扁平 fields 切片里筛出成员表条目（成员字段以"姓名"键为特征，
// 任务/版本等其他表的推送不携带该键）。
func memberFields(list []map[string]any) []map[string]any {
	var out []map[string]any
	for _, f := range list {
		if _, ok := f["姓名"]; ok {
			out = append(out, f)
		}
	}
	return out
}

// 成员表是单向镜像（pulse → 飞书）：首推全量建行，未变更行免调用；
// 容量/备注变更 → PUT 更新；飞书侧修改不回流，成员维护走 CLI/MCP。
func TestSyncMembersMirror(t *testing.T) {
	s, p, actor := syncEnv(t)

	// 补配置成员表（模拟 v1.1.1+ bind 建出的成员表）
	tables, err := s.GetFeishuTables(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	tables.Members = "tblMem"
	if err := s.SaveFeishuTables(p.ID, tables); err != nil {
		t.Fatal(err)
	}

	// 成员：tester 来自 syncEnv，再加一个 agent
	codex, err := s.GetOrCreateMember("codex", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemberCapacity(codex.ID, 3); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{createIDs: map[string][]string{"tblMem": {"recM1", "recM2"}}}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	mf := memberFields(fake.createdFields)
	if len(mf) != 2 {
		t.Fatalf("应推送 2 名成员: %d", len(mf))
	}
	found := map[string]map[string]any{}
	for _, f := range mf {
		if name, ok := f["姓名"].(string); ok {
			found[name] = f
		}
	}
	if got, ok := found["codex"]["周容量人日"].(float64); !ok || got != 3 {
		t.Fatalf("codex 周容量 = %v, want 3", got)
	}
	if got := found["codex"]["类型"]; got != "agent" {
		t.Fatalf("codex 类型 = %v, want agent", got)
	}

	// 第二次 sync：无变更 → 零成员调用
	before := len(memberFields(fake.createdFields)) + len(memberFields(fake.updatedFields))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	after := len(memberFields(fake.createdFields)) + len(memberFields(fake.updatedFields))
	if before != after {
		t.Fatalf("无变更不应产生成员调用: %d → %d", before, after)
	}

	// 容量变更 → PUT 更新
	fake.updatedFields = nil
	if err := s.SetMemberCapacity(codex.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	if len(memberFields(fake.updatedFields)) == 0 {
		t.Fatal("容量变更应产生 RecordUpdate")
	}
}

// 未配置成员表（Members id 为空，旧项目/采用模式）时成员镜像静默跳过。
func TestSyncMembersSkippedWhenUnconfigured(t *testing.T) {
	s, p, actor := syncEnv(t) // v11Tables() 不含 Members
	fake := &fakeAPI{}
	if _, err := SyncProject(context.Background(), clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if call.method == "TableCreate" && len(call.args) > 1 && call.args[1] == "成员表" {
			t.Fatal("未配置成员表时不应出现成员表相关调用")
		}
	}
	if mf := memberFields(fake.createdFields); len(mf) != 0 {
		t.Fatalf("未配置成员表时不应推送成员: %d", len(mf))
	}
}
