package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/zhangyi/pulse/internal/model"
)

// ErrDuplicateVersion 表示项目内版本名唯一约束（UNIQUE(project_id, name)）冲突（errors.Is 判断）。
var ErrDuplicateVersion = errors.New("版本已存在")

// validVersionStatuses 与 schema 的 CHECK 约束保持一致。
var validVersionStatuses = map[string]bool{
	"planned": true, "in_dev": true, "released": true, "shipped": true,
}

// VersionChanges UpdateVersion 的增量变更集；nil 指针 = 不修改该字段。
type VersionChanges struct {
	Status, TargetDate, Notes *string
}

const versionCols = `id, project_id, name, target_date, status, notes, bitable_record_id, bitable_synced_hash`

// scanVersion 从一行结果扫描出 model.Version。
func scanVersion(scan func(dest ...any) error) (model.Version, error) {
	var v model.Version
	if err := scan(&v.ID, &v.ProjectID, &v.Name, &v.TargetDate, &v.Status,
		&v.Notes, &v.BitableRecordID, &v.BitableSyncedHash); err != nil {
		return model.Version{}, err
	}
	return v, nil
}

// versionActivity 组装一条 version 实体的活动记录（与 taskActivity 同构）。
func versionActivity(projectID, versionID int64, actor model.Member, behalf *model.Member, action, detail string) model.Activity {
	var onBehalfOf int64
	if behalf != nil {
		onBehalfOf = behalf.ID
	}
	return model.Activity{
		ProjectID: projectID, ActorID: actor.ID, ActorType: actor.Type,
		OnBehalfOf: onBehalfOf, Action: action, EntityType: "version",
		EntityID: versionID, Detail: detail,
	}
}

// isUniqueViolation 识别 SQLite 唯一约束冲突（modernc.org/sqlite 错误文本稳定含此串）。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateVersion 创建版本并与 create 活动同事务落库；status 留空补 planned，
// 非法报错；项目内版本名重复（UNIQUE(project_id, name)）映射为 ErrDuplicateVersion。
func (s *Store) CreateVersion(v model.Version, actor model.Member, behalf *model.Member) (model.Version, error) {
	if strings.TrimSpace(v.Name) == "" {
		return model.Version{}, fmt.Errorf("版本名不能为空")
	}
	status := v.Status
	if status == "" {
		status = "planned"
	}
	if !validVersionStatuses[status] {
		return model.Version{}, fmt.Errorf("status 必须为 planned|in_dev|released|shipped，收到 %q", status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Version{}, fmt.Errorf("begin create version: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO versions (project_id, name, target_date, status, notes)
		VALUES (?, ?, ?, ?, ?)`,
		v.ProjectID, v.Name, v.TargetDate, status, v.Notes)
	if err != nil {
		if isUniqueViolation(err) {
			return model.Version{}, fmt.Errorf("%w: %s", ErrDuplicateVersion, v.Name)
		}
		return model.Version{}, fmt.Errorf("insert version %q: %w", v.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Version{}, fmt.Errorf("insert version %q: %w", v.Name, err)
	}
	if err := insertActivity(tx, versionActivity(v.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Version{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Version{}, fmt.Errorf("commit create version: %w", err)
	}
	created, _, err := s.GetVersion(id)
	return created, err
}

// GetVersion 按 ID 查询版本；不存在时 found=false 且无错误。
func (s *Store) GetVersion(id int64) (model.Version, bool, error) {
	row := s.db.QueryRow(`SELECT `+versionCols+` FROM versions WHERE id = ?`, id)
	v, err := scanVersion(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Version{}, false, nil
	}
	if err != nil {
		return model.Version{}, false, fmt.Errorf("get version id=%d: %w", id, err)
	}
	return v, true, nil
}

// ListVersions 列出项目内版本（按 id 升序）。
func (s *Store) ListVersions(projectID int64) ([]model.Version, error) {
	rows, err := s.db.Query(`SELECT `+versionCols+` FROM versions WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	var vs []model.Version
	for rows.Next() {
		v, err := scanVersion(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		vs = append(vs, v)
	}
	return vs, rows.Err()
}

// UpdateVersion 在单个事务内完成版本更新（与 UpdateTask 同构）：读旧行 → 逐字段
// 比对 → 拼 UPDATE → 逐字段落活动 → 提交。
//   - status 变更记 action="update_status"；版本无 done→非 done 的 reopen 规则，
//     shipped 改回 planned 同样记 update_status；
//   - target_date/notes 变更记 action="update"；
//   - 无变更（含同值写入）为 no-op，不落活动。
func (s *Store) UpdateVersion(id int64, ch VersionChanges, actor model.Member, behalf *model.Member) (model.Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Version{}, fmt.Errorf("begin update version: %w", err)
	}
	defer tx.Rollback()
	old, err := scanVersion(tx.QueryRow(`SELECT `+versionCols+` FROM versions WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Version{}, fmt.Errorf("版本不存在: id=%d", id)
	}
	if err != nil {
		return model.Version{}, fmt.Errorf("load version id=%d: %w", id, err)
	}

	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, versionActivity(old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
	}

	if ch.Status != nil && *ch.Status != old.Status {
		if !validVersionStatuses[*ch.Status] {
			return model.Version{}, fmt.Errorf("status 必须为 planned|in_dev|released|shipped，收到 %q", *ch.Status)
		}
		sets = append(sets, "status = ?")
		args = append(args, *ch.Status)
		change("status", "update_status", old.Status, *ch.Status)
	}
	if ch.TargetDate != nil && *ch.TargetDate != old.TargetDate {
		sets = append(sets, "target_date = ?")
		args = append(args, *ch.TargetDate)
		change("target_date", "update", old.TargetDate, *ch.TargetDate)
	}
	if ch.Notes != nil && *ch.Notes != old.Notes {
		sets = append(sets, "notes = ?")
		args = append(args, *ch.Notes)
		change("notes", "update", old.Notes, *ch.Notes)
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE versions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Version{}, fmt.Errorf("update version id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.Version{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Version{}, fmt.Errorf("commit update version id=%d: %w", id, err)
	}
	v, _, err := s.GetVersion(id)
	return v, err
}
