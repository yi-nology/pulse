package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultActor != "" || c.Sync.StaleMinutes != 10 || c.Feishu.AppID != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadFromFileAndSecretEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"default_actor: zhangyi\nfeishu:\n  app_id: cli_a1\n  app_secret: fromfile\nsync:\n  stale_minutes: 30\n"), 0o600)
	t.Setenv("PULSE_FEISHU_APP_SECRET", "fromenv")
	c, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultActor != "zhangyi" || c.Feishu.AppID != "cli_a1" || c.Sync.StaleMinutes != 30 {
		t.Fatalf("got %+v", c)
	}
	if c.Feishu.AppSecret != "fromenv" {
		t.Fatalf("env must override file, got %q", c.Feishu.AppSecret)
	}
}

func TestDefaultPathHonorsPulseHome(t *testing.T) {
	t.Setenv("PULSE_HOME", "/tmp/pulse-x")
	if got := DefaultPath(); got != "/tmp/pulse-x/config.yaml" {
		t.Fatalf("got %q", got)
	}
}
