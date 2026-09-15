package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS members (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL DEFAULT 'human' CHECK(type IN ('human','agent')),
  capacity_days_per_week REAL NOT NULL DEFAULT 5,
  notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
);
CREATE TABLE IF NOT EXISTS projects (
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
);
CREATE TABLE IF NOT EXISTS versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,
  target_date TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','in_dev','released','shipped')),
  notes TEXT NOT NULL DEFAULT '',
  bitable_record_id TEXT NOT NULL DEFAULT '',
  bitable_synced_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
  UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  assignee_id INTEGER REFERENCES members(id),
  status TEXT NOT NULL DEFAULT 'todo' CHECK(status IN ('backlog','todo','in_progress','blocked','done')),
  priority INTEGER NOT NULL DEFAULT 3,
  estimate_days REAL NOT NULL DEFAULT 0,
  start_date TEXT NOT NULL DEFAULT '',
  due_date TEXT NOT NULL DEFAULT '',
  version_id INTEGER REFERENCES versions(id),
  requirement_id INTEGER,
  bitable_record_id TEXT NOT NULL DEFAULT '',
  bitable_synced_hash TEXT NOT NULL DEFAULT '',
  archived INTEGER NOT NULL DEFAULT 0,
  status_changed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
);
CREATE TABLE IF NOT EXISTS dependencies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id),
  depends_on_task_id INTEGER NOT NULL REFERENCES tasks(id),
  type TEXT NOT NULL DEFAULT 'FS'
);
-- 同向依赖唯一：兜底 AddDependency 预查询与插入之间的并发窗口（TOCTOU），
-- 对已存在重复行的旧库建索引会失败，属预期数据修复信号。
CREATE UNIQUE INDEX IF NOT EXISTS idx_dependencies_pair ON dependencies(task_id, depends_on_task_id);
CREATE TABLE IF NOT EXISTS activity (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER REFERENCES projects(id),
  actor_id INTEGER NOT NULL REFERENCES members(id),
  actor_type TEXT NOT NULL,
  on_behalf_of INTEGER REFERENCES members(id),
  action TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id INTEGER NOT NULL,
  detail TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now'))
);
CREATE TABLE IF NOT EXISTS sync_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// 每个池化连接都是一块独立的空内存库；多连接会导致"no such table"，
		// 因此 :memory: 必须限制为单连接（文件路径库保持默认连接池 + busy_timeout）。
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) Close() error { return s.db.Close() }
