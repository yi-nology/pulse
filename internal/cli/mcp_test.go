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
