package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// lastVersionActivity 取项目内最后一条 version 实体活动的 action/detail。
func lastVersionActivity(t *testing.T, s *store.Store, projectID int64) (string, string) {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	var action, detail string
	for _, a := range acts {
		if a.EntityType == "version" {
			found, action, detail = true, a.Action, a.Detail
		}
	}
	if !found {
		t.Fatalf("no version activity found: %+v", acts)
	}
	return action, detail
}

// requireLastVersionActivity 断言项目内最后一条 version 实体活动的 action 与 detail。
func requireLastVersionActivity(t *testing.T, s *store.Store, projectID int64, action, detailSub string) {
	t.Helper()
	gotAction, gotDetail := lastVersionActivity(t, s, projectID)
	if gotAction != action || !strings.Contains(gotDetail, detailSub) {
		t.Fatalf("last version activity = %q / %q, want %q / contains %q", gotAction, gotDetail, action, detailSub)
	}
}

func TestVersionAddListUpdate(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}

	// add：缺省 planned，--target 可选
	out, errOut, err := runCLI(t, "version", "add", "v1.0", "--project", "demo", "--target", "2026-10-01")
	if err != nil {
		t.Fatalf("version add failed: %v stderr=%s", err, errOut)
	}
	if want := "版本已创建: v1.0 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	if _, _, err := runCLI(t, "version", "add", "v1.1", "--project", "demo"); err != nil {
		t.Fatal(err)
	}

	// list：表头 + 字段
	out, _, err = runCLI(t, "version", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t名称\t状态\t目标日期\t备注", "v1.0\tplanned\t2026-10-01", "v1.1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}

	// update --status：活动走 update_status（版本无 reopen 语义）
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}
	out, _, err = runCLI(t, "version", "update", "1", "--status", "in_dev")
	if err != nil {
		t.Fatalf("version update failed: %v", err)
	}
	if want := "版本已更新: v1.0 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	requireLastVersionActivity(t, s, p.ID, "update_status", `"from":"planned","to":"in_dev"`)
	// update --target 走 update
	if _, _, err := runCLI(t, "version", "update", "1", "--target", "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	requireLastVersionActivity(t, s, p.ID, "update", `"field":"target_date","from":"2026-10-01","to":"2026-11-01"`)

	// 列表反映新状态与日期
	out, _, err = runCLI(t, "version", "list", "--project", "demo")
	if err != nil || !strings.Contains(out, "v1.0\tin_dev\t2026-11-01") {
		t.Fatalf("list must show updated version: out=%q err=%v", out, err)
	}

	// 错误路径
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"缺 --project", []string{"version", "add", "v2"}, "必须提供 --project"},
		{"非法状态", []string{"version", "update", "1", "--status", "doing"}, "status 必须为"},
		{"非法日期", []string{"version", "update", "1", "--target", "下月"}, "YYYY-MM-DD"},
		{"版本不存在", []string{"version", "update", "99", "--status", "shipped"}, "版本不存在: id=99"},
	}
	for _, c := range cases {
		_, errOut, err := runCLI(t, c.args...)
		if err == nil {
			t.Fatalf("%s: must fail", c.name)
		}
		if !strings.Contains(errOut, c.want) {
			t.Fatalf("%s: stderr %q must contain %q", c.name, errOut, c.want)
		}
	}

	// 重复版本名：UNIQUE(project_id,name) 冲突映射为中文文案
	_, errOut, err = runCLI(t, "version", "add", "v1.0", "--project", "demo")
	if err == nil {
		t.Fatal("duplicate version name must fail")
	}
	if !strings.Contains(errOut, "版本已存在: v1.0") {
		t.Fatalf("stderr %q must contain %q", errOut, "版本已存在: v1.0")
	}
}

func TestTaskDepCommand(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "add", "任务甲", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "add", "任务乙", "--project", "demo"); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runCLI(t, "task", "dep", "2", "--on", "1")
	if err != nil {
		t.Fatalf("task dep failed: %v stderr=%s", err, errOut)
	}
	if want := "依赖已添加: 任务 2 依赖任务 1"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	var deps []model.Dependency
	deps, err = s.ListDependencies(1)
	if err != nil || len(deps) != 1 || deps[0].TaskID != 2 || deps[0].DependsOnTaskID != 1 {
		t.Fatalf("dependency not persisted: %+v err=%v", deps, err)
	}

	// 错误路径
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"自依赖", []string{"task", "dep", "1", "--on", "1"}, "任务不能依赖自己"},
		{"重复依赖", []string{"task", "dep", "2", "--on", "1"}, "依赖关系已存在"},
		{"任务不存在", []string{"task", "dep", "99", "--on", "1"}, "任务不存在: id=99"},
		{"被依赖方不存在", []string{"task", "dep", "1", "--on", "99"}, "任务不存在: id=99"},
		{"非法 ID", []string{"task", "dep", "abc", "--on", "1"}, "任务 ID 须为整数"},
	}
	for _, c := range cases {
		_, errOut, err := runCLI(t, c.args...)
		if err == nil {
			t.Fatalf("%s: must fail", c.name)
		}
		if !strings.Contains(errOut, c.want) {
			t.Fatalf("%s: stderr %q must contain %q", c.name, errOut, c.want)
		}
	}
}
