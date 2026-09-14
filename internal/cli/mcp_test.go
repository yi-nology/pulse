package cli

import (
	"strings"
	"testing"
)

// TestMCPRequiresActor PULSE_ACTOR 为空时拒绝启动并给出中文提示。
func TestMCPRequiresActor(t *testing.T) {
	_, _, _ = testEnv(t)
	t.Setenv("PULSE_ACTOR", "")
	_, errOut, err := runCLI(t, "mcp")
	if err == nil {
		t.Fatal("pulse mcp without PULSE_ACTOR must fail")
	}
	if !strings.Contains(err.Error(), "启动 MCP server 需设置 PULSE_ACTOR") {
		t.Fatalf("error %q must carry the guidance text", err)
	}
	if errOut == "" {
		t.Fatal("stderr must carry the guidance text")
	}
}

// TestPublishReportForMCPReportsFreshDoc：未绑定文档的项目经 publish_feishu 桥接
// 自动建档后，返回的 doc 必须非空且与 store 落库一致——PublishReport 按值接收项目，
// 补建的 token 只写了函数内副本，桥接必须重读项目取真实值。
func TestPublishReportForMCPReportsFreshDoc(t *testing.T) {
	s, cfg := feishuTestEnv(t)
	p, err := s.CreateProject("demo", "演示", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, docN, blockN, _ := newFakeFeishu(t)
	// 已绑 base、未绑文档（采用模式省略 --doc 的现场）
	p.FeishuBitableAppToken = "appB"
	p.FeishuTaskTableID = "tblTask"
	p.FeishuVersionTableID = "tblVer"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}

	out, err := publishReportForMCP(s, cfg, "demo", "weekly")
	if err != nil {
		t.Fatalf("publishReportForMCP: %v", err)
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", out)
	}
	saved, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	if saved.FeishuDocToken == "" {
		t.Fatal("store 中 doc token 未回写")
	}
	if res["doc"] == "" || res["doc"] != saved.FeishuDocToken {
		t.Fatalf("返回 doc = %v, want 非空且与 store 一致（%q）", res["doc"], saved.FeishuDocToken)
	}
	if n := docN.Load(); n != 1 {
		t.Fatalf("DocCreate 次数 = %d, want 1（publish 自动补建）", n)
	}
	if n := blockN.Load(); n != 1 {
		t.Fatalf("BlockAppend 次数 = %d, want 1", n)
	}
}
