package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Feishu struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

type Sync struct {
	StaleMinutes int `yaml:"stale_minutes"`
}

type Config struct {
	DefaultActor string `yaml:"default_actor"`
	Feishu       Feishu `yaml:"feishu"`
	Sync         Sync   `yaml:"sync"`
}

func DefaultPath() string {
	if h := os.Getenv("PULSE_HOME"); h != "" {
		return filepath.Join(h, "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pulse", "config.yaml")
}

func DBPath() string {
	if h := os.Getenv("PULSE_HOME"); h != "" {
		return filepath.Join(h, "pulse.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pulse", "pulse.db")
}

// Load 读取配置；文件不存在时返回默认值。环境变量 PULSE_FEISHU_APP_SECRET 优先于文件。
func Load(path string) (*Config, error) {
	c := &Config{Sync: Sync{StaleMinutes: 10}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, c); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if c.Sync.StaleMinutes <= 0 {
		c.Sync.StaleMinutes = 10
	}
	if s := os.Getenv("PULSE_FEISHU_APP_SECRET"); s != "" {
		c.Feishu.AppSecret = s
	}
	return c, nil
}
