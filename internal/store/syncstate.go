package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
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

// syncedAtStamp 返回该实体表需要附加的 ", synced_at = ?" 子句与取值（now UTC，与
// activitiesLayout 的落库文本格式一致）。synced_at 记录本行上次与飞书收敛的时刻，
// 是 sync 覆盖警告（E2E-3）的判定基准；版本表无该列（返回空，警告仅对有列实体适用），
// 任务表与 v1.1 六实体表均有。
func syncedAtStamp(entity string) (string, []any) {
	if entity == "version" {
		return "", nil
	}
	return ", synced_at = ?", []any{time.Now().UTC().Format(activitiesLayout)}
}

// MarkSyncedHash 回写实体的 bitable_synced_hash（record_id 不变时用）；任务表同时
// 刷新 synced_at。entity 取 "task" 或 "version"；实体不存在时报错。
func (s *Store) MarkSyncedHash(entity string, id int64, hash string) error {
	table, err := syncEntityTable(entity)
	if err != nil {
		return err
	}
	stamp, stampArgs := syncedAtStamp(entity)
	args := append([]any{hash}, stampArgs...)
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE `+table+` SET bitable_synced_hash = ?`+stamp+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("mark synced %s#%d: %w", entity, id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("mark synced: %s 不存在 id=%d", entity, id)
	}
	return nil
}

// MarkSyncedRecord 回写实体的 bitable_record_id + bitable_synced_hash
// （push 新建回填 record_id、pull 合入链接远端记录时用）；任务表同时刷新 synced_at。
func (s *Store) MarkSyncedRecord(entity string, id int64, recordID, hash string) error {
	table, err := syncEntityTable(entity)
	if err != nil {
		return err
	}
	stamp, stampArgs := syncedAtStamp(entity)
	args := append([]any{recordID, hash}, stampArgs...)
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE `+table+` SET bitable_record_id = ?, bitable_synced_hash = ?`+stamp+` WHERE id = ?`,
		args...)
	if err != nil {
		return fmt.Errorf("mark synced %s#%d: %w", entity, id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("mark synced: %s 不存在 id=%d", entity, id)
	}
	return nil
}

// syncEntityTable 把实体名映射到表名（v1.1 起含六实体；实体名与 activity.entity_type 一致）。
func syncEntityTable(entity string) (string, error) {
	switch entity {
	case "task":
		return "tasks", nil
	case "version":
		return "versions", nil
	case "requirement":
		return "requirements", nil
	case "review":
		return "reviews", nil
	case "meeting":
		return "meetings", nil
	case "bug":
		return "bugs", nil
	case "test_submission":
		return "test_submissions", nil
	case "release":
		return "releases", nil
	default:
		return "", fmt.Errorf("markSynced: 未知实体 %q", entity)
	}
}

// syncEntityLabels 六实体的中文名（错误信息用，与各实体文件错误文案一致）。
var syncEntityLabels = map[string]string{
	"requirement": "需求", "review": "评审", "meeting": "会议",
	"bug": "bug", "test_submission": "提测单", "release": "发版",
}

// SoftDeleteSyncEntity 软删 v1.1 六实体行（archived 置 1、行保留、刷新 updated_at、
// 落 archive 活动），与 SoftDeleteTask 同语义；供 sync pull 侧墓碑合入调用。
// 已归档的行幂等返回（不重复落活动）；行不存在或实体不支持软删时报错。
func (s *Store) SoftDeleteSyncEntity(entity string, id int64, actor model.Member, behalf *model.Member) error {
	table, err := syncEntityTable(entity)
	if err != nil {
		return err
	}
	if _, ok := syncEntityLabels[entity]; !ok {
		return fmt.Errorf("soft delete: 实体 %q 无软删语义", entity)
	}
	label := syncEntityLabels[entity]
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin soft delete %s: %w", entity, err)
	}
	defer tx.Rollback()
	var projectID int64
	var archived int
	err = tx.QueryRow(`SELECT project_id, archived FROM `+table+` WHERE id = ?`, id).Scan(&projectID, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s不存在: id=%d", label, id)
	}
	if err != nil {
		return fmt.Errorf("load %s id=%d: %w", entity, id, err)
	}
	if archived != 0 {
		return tx.Commit() // 幂等：已归档
	}
	if _, err := tx.Exec(`UPDATE `+table+` SET archived = 1, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(activitiesLayout), id); err != nil {
		return fmt.Errorf("soft delete %s id=%d: %w", entity, id, err)
	}
	if err := insertActivity(tx, entityActivity(entity, projectID, id, actor, behalf,
		"archive", changeDetail("archived", false, true))); err != nil {
		return err
	}
	return tx.Commit()
}
