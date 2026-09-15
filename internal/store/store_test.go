package store

import (
	"database/sql"
	"testing"

	_ "github.com/zhangyi/pulse/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTest(t)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil { // 二次执行不得报错
		t.Fatal(err)
	}
}

func TestOpenBadPathFails(t *testing.T) {
	if _, err := Open("/nonexistent-dir-x/pulse.db"); err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

// TestMigrateLegacyDBUpgradesToV11 模拟 v1.0 老库（tasks 无 requirement_id、无六实体表）：
// 迁移须补建六张新表、为 tasks 补 requirement_id 列，且可重复执行。
func TestMigrateLegacyDBUpgradesToV11(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
	CREATE TABLE members (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
	CREATE TABLE projects (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE);
	CREATE TABLE tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER, title TEXT NOT NULL);
	`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer s.Close()
	for _, table := range []string{"requirements", "reviews", "meetings", "bugs", "test_submissions", "releases"} {
		var name string
		if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s must exist after migrate: %v", table, err)
		}
	}
	if _, err := s.db.Exec(`SELECT requirement_id, synced_at FROM tasks LIMIT 1`); err != nil {
		t.Fatalf("tasks must gain requirement_id/synced_at columns: %v", err)
	}
	if err := s.migrate(); err != nil { // 二次迁移不得报错
		t.Fatal(err)
	}
}
