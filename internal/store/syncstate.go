package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// GetSyncState 读取 sync_state 表中 key 的值；不存在时 found=false 且无错误。
// sync_state 存放同步游标（pull 水位、last_pull 时间戳等），key 由调用方约定。
func (s *Store) GetSyncState(key string) (string, bool, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get sync_state %q: %w", key, err)
	}
	return val, true, nil
}

// SetSyncState 写入（upsert）sync_state 表的一组键值。
func (s *Store) SetSyncState(key, val string) error {
	_, err := s.db.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, val)
	if err != nil {
		return fmt.Errorf("set sync_state %q: %w", key, err)
	}
	return nil
}

// MarkSyncedHash 回写实体的 bitable_synced_hash（record_id 不变时用）。
// entity 取 "task" 或 "version"；实体不存在时报错。
func (s *Store) MarkSyncedHash(entity string, id int64, hash string) error {
	table, err := syncEntityTable(entity)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE `+table+` SET bitable_synced_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("mark synced %s#%d: %w", entity, id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("mark synced: %s 不存在 id=%d", entity, id)
	}
	return nil
}

// MarkSyncedRecord 回写实体的 bitable_record_id + bitable_synced_hash
// （push 新建回填 record_id、pull 合入链接远端记录时用）。
func (s *Store) MarkSyncedRecord(entity string, id int64, recordID, hash string) error {
	table, err := syncEntityTable(entity)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE `+table+` SET bitable_record_id = ?, bitable_synced_hash = ? WHERE id = ?`,
		recordID, hash, id)
	if err != nil {
		return fmt.Errorf("mark synced %s#%d: %w", entity, id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("mark synced: %s 不存在 id=%d", entity, id)
	}
	return nil
}

// syncEntityTable 把实体名映射到表名。
func syncEntityTable(entity string) (string, error) {
	switch entity {
	case "task":
		return "tasks", nil
	case "version":
		return "versions", nil
	default:
		return "", fmt.Errorf("markSynced: 未知实体 %q", entity)
	}
}
