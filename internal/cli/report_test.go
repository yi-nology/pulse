package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportGanttToStdout(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "add", "骨架", "--project", "demo",
		"--start", "2026-09-01", "--due", "2026-09-10"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "report", "gantt", "--project", "demo")
	if err != nil {
		t.Fatalf("report gantt failed: %v", err)
	}
	if !strings.Contains(out, "<title>甘特图 · demo</title>") {
		t.Fatalf("gantt stdout must contain self-contained HTML title, got:\n%s", out)
	}
	if !strings.Contains(out, "#1 骨架") {
		t.Fatalf("gantt stdout must contain task bar label, got:\n%s", out)
	}
}

func TestReportWeeklyToOutFile(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "weekly.md")
	out, _, err := runCLI(t, "report", "weekly", "--project", "demo", "--out", outPath)
	if err != nil {
		t.Fatalf("report weekly failed: %v", err)
	}
	if !strings.Contains(out, "报表已写入") {
		t.Fatalf("stdout must confirm file write, got:\n%s", out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# 周报 · demo") || !strings.Contains(string(data), "## 本周完成") {
		t.Fatalf("weekly file content unexpected:\n%s", data)
	}
}

func TestReportRequiresProject(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "report", "workload"); err == nil {
		t.Fatal("report without --project must fail")
	}
	if _, _, err := runCLI(t, "report", "workload", "--project", "nope"); err == nil {
		t.Fatal("report with unknown project must fail")
	}
}
