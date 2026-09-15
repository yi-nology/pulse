package store

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// migrate_legacy_test.go：真实老库升级路径的回归（v1.2 审查修复）。
// 根因：idx_requirements_uid 曾写在 schema 常量里（migrate 主执行），而
// ALTER TABLE requirements ADD COLUMN uid 在其后的 Go 分支才执行——老库
// （v1.1 时代、requirements 无 uid 列）在加列前建索引直接失败
//（"migrate: SQL logic error: no such column: uid"），pulse 完全不可用。
// 本测试用 v1.1 时代的 schema 手工建库（requirements 无 uid 列/无索引，
// members 无同步镜像列、projects 无 feishu_tables_json、tasks 无
// synced_at/requirement_id——一并对齐全部容忍 ALTER 的顺序），断言：
// 升级成功、索引存在且生效、旧数据保留可回填、二次 Open 幂等。

var wantUIDHex = regexp.MustCompile(`^[0-9a-f]{32}$`)

// seedLegacyV11DB 按 v1.2 之前的 schema 手工建库并塞入旧行。
func seedLegacyV11DB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl := []string{
		// members：无 bitable_record_id / bitable_synced_hash（v1.1 由容忍 ALTER 补）
		`CREATE TABLE members (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  name TEXT NOT NULL UNIQUE,
		  type TEXT NOT NULL DEFAULT 'human' CHECK(type IN ('human','agent')),
		  capacity_days_per_week REAL NOT NULL DEFAULT 5,
		  notes TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
		)`,
		// projects：无 feishu_tables_json（v1.1 由容忍 ALTER 补）
		`CREATE TABLE projects (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  key TEXT NOT NULL UNIQUE,
		  name TEXT NOT NULL DEFAULT '',
		  description TEXT NOT NULL DEFAULT '',
		  status TEXT NOT NULL DEFAULT 'active',
		  feishu_doc_token TEXT NOT NULL DEFAULT '',
		  feishu_bitable_app_token TEXT NOT NULL DEFAULT '',
		  feishu_task_table_id TEXT NOT NULL DEFAULT '',
		  feishu_version_table_id TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
		)`,
		// requirements：无 uid 列、无 idx_requirements_uid（v1.1 实盘形态，本回归的靶心）
		`CREATE TABLE requirements (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  project_id INTEGER NOT NULL REFERENCES projects(id),
		  title TEXT NOT NULL,
		  description TEXT NOT NULL DEFAULT '',
		  status TEXT NOT NULL DEFAULT 'proposed' CHECK(status IN ('proposed','reviewing','accepted','in_dev','delivered','rejected')),
		  priority INTEGER NOT NULL DEFAULT 3,
		  owner_id INTEGER REFERENCES members(id),
		  source TEXT NOT NULL DEFAULT '',
		  feishu_doc_token TEXT NOT NULL DEFAULT '',
		  bitable_record_id TEXT NOT NULL DEFAULT '',
		  bitable_synced_hash TEXT NOT NULL DEFAULT '',
		  synced_at TEXT NOT NULL DEFAULT '',
		  archived INTEGER NOT NULL DEFAULT 0,
		  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
		  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
		)`,
		`INSERT INTO members (name, type) VALUES ('tester', 'human')`,
		`INSERT INTO projects (key, name) VALUES ('demo', '演示项目')`,
		`INSERT INTO requirements (project_id, title, status, priority) VALUES (1, '老库需求', 'proposed', 3)`,
		`INSERT INTO requirements (project_id, title, status, priority) VALUES (1, '老库需求二', 'reviewing', 2)`,
	}
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v\nstmt: %s", err, stmt)
		}
	}
}

func TestMigrateLegacyV11Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacyV11DB(t, path)

	// 修复前：migrate 在 schema 阶段建 idx_requirements_uid 报
	// "SQL logic error: no such column: uid"，Open 整体失败
	s, err := Open(path)
	if err != nil {
		t.Fatalf("老库升级必须成功: %v", err)
	}
	defer s.Close()

	// 索引已建
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_requirements_uid'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idx_requirements_uid 应存在: n=%d err=%v", n, err)
	}
	// 唯一索引对非空 uid 生效（旧行先回填再撞）
	uid1, err := s.EnsureRequirementUID(1)
	if err != nil || !wantUIDHex.MatchString(uid1) {
		t.Fatalf("旧行 uid 应可回填: %q err=%v", uid1, err)
	}
	uid2, err := s.EnsureRequirementUID(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE requirements SET uid = ? WHERE id = 2`, uid1); err == nil {
		t.Fatal("重复 uid 应被部分唯一索引拦截")
	}
	if uid1 == uid2 {
		t.Fatalf("回填必须互不相同: %q", uid1)
	}
	// 旧数据保留、新链路可用（CreateRequirement 走新列）
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequirement(model.Requirement{ProjectID: 1, Title: "升级后新建"}, actor, nil)
	if err != nil {
		t.Fatalf("升级后建需求必须可用: %v", err)
	}
	if !wantUIDHex.MatchString(r.UID) {
		t.Fatalf("新行应生成 UID: %q", r.UID)
	}
	rs, err := s.ListRequirements(1, "")
	if err != nil || len(rs) != 3 {
		t.Fatalf("旧数据应保留: %+v err=%v", rs, err)
	}

	// 二次 Open 幂等（duplicate column 容忍 + IF NOT EXISTS 索引）
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("二次 migrate 必须幂等: %v", err)
	}
	defer s2.Close()
	rs2, err := s2.ListRequirements(1, "")
	if err != nil || len(rs2) != 3 || rs2[0].UID != uid1 {
		t.Fatalf("二次升级后数据应原样: %+v err=%v", rs2, err)
	}
}
