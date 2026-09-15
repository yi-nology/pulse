package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validReleaseStatuses 与 schema 的 CHECK 约束保持一致。
var validReleaseStatuses = map[string]bool{
	"preparing": true, "testing": true, "released": true, "rolled_back": true,
}

// ReleaseChanges UpdateRelease 的增量变更集；nil 指针 = 不修改该字段。
// FeishuDocToken 供发版记录文档写回（Task 3）。released_at 由 store 在进入 released
// 时自动补记，不作为可改字段。
type ReleaseChanges struct {
	Status, Notes, FeishuDocToken *string
}

const releaseCols = `id, project_id, version_id, status, release_manager_id, released_at,
	feishu_doc_token, notes, bitable_record_id, bitable_synced_hash, synced_at, archived,
	created_at, updated_at`

// scanRelease 从一行结果扫描出 model.Release（release_manager_id 可空，NULL 映射 0）。
func scanRelease(scan func(dest ...any) error) (model.Release, error) {
	var r model.Release
	var managerID sql.NullInt64
	var archived int
	if err := scan(&r.ID, &r.ProjectID, &r.VersionID, &r.Status, &managerID, &r.ReleasedAt,
		&r.FeishuDocToken, &r.Notes, &r.BitableRecordID, &r.BitableSyncedHash, &r.SyncedAt,
		&archived, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return model.Release{}, err
	}
	r.ReleaseManagerID = managerID.Int64
	r.Archived = archived != 0
	return r, nil
}

// CreateRelease 创建发版记录并落 create 活动（与写入同一事务）；status 留空补
// preparing，非法报错；release_manager 留空归当前操作者；以 released 创建时补记
// released_at。
func (s *Store) CreateRelease(r model.Release, actor model.Member, behalf *model.Member) (model.Release, error) {
	status := r.Status
	if status == "" {
		status = "preparing"
	}
	if !validReleaseStatuses[status] {
		return model.Release{}, fmt.Errorf("status 必须为 preparing|testing|released|rolled_back，收到 %q", status)
	}
	managerID := r.ReleaseManagerID
	if managerID == 0 { // 未显式指定发布负责人时归当前操作者
		managerID = actor.ID
	}
	var releasedAt string
	if status == "released" {
		releasedAt = time.Now().UTC().Format(activitiesLayout)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Release{}, fmt.Errorf("begin create release: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO releases
		(project_id, version_id, status, release_manager_id, released_at, notes)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.VersionID, status, managerID, releasedAt, r.Notes)
	if err != nil {
		return model.Release{}, fmt.Errorf("insert release: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Release{}, fmt.Errorf("insert release: %w", err)
	}
	if err := insertActivity(tx, entityActivity("release", r.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Release{}, fmt.Errorf("commit create release: %w", err)
	}
	row := s.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id)
	got, err := scanRelease(row.Scan)
	if err != nil {
		return model.Release{}, fmt.Errorf("get release id=%d: %w", id, err)
	}
	return got, nil
}

// GetRelease 按 ID 查询发版记录；不存在时 found=false 且无错误。
func (s *Store) GetRelease(id int64) (model.Release, bool, error) {
	row := s.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id)
	r, err := scanRelease(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Release{}, false, nil
	}
	if err != nil {
		return model.Release{}, false, fmt.Errorf("get release id=%d: %w", id, err)
	}
	return r, true, nil
}

// ListReleases 按项目列出发版记录（按 id 升序）。
func (s *Store) ListReleases(projectID int64) ([]model.Release, error) {
	rows, err := s.db.Query(`SELECT `+releaseCols+` FROM releases WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	var rs []model.Release
	for rows.Next() {
		r, err := scanRelease(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// UpdateRelease 在单个事务内完成发版更新（与 UpdateTask 同构）：
//   - status 变更记 action="update_status"；进入 released 补记 released_at（派生值，
//     不单独落活动）；无 reopen 规则（released→rolled_back 照常记 update_status）；
//   - notes/feishu_doc_token 变更记 action="update"；
//   - 任何变更刷新 updated_at；无变更（含同值写入）为 no-op，不刷新、不落活动。
func (s *Store) UpdateRelease(id int64, ch ReleaseChanges, actor model.Member, behalf *model.Member) (model.Release, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Release{}, fmt.Errorf("begin update release: %w", err)
	}
	defer tx.Rollback()
	old, err := scanRelease(tx.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Release{}, fmt.Errorf("发版不存在: id=%d", id)
	}
	if err != nil {
		return model.Release{}, fmt.Errorf("load release id=%d: %w", id, err)
	}

	now := time.Now().UTC().Format(activitiesLayout)
	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, entityActivity("release", old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
	}

	newStatus := old.Status
	if ch.Status != nil && *ch.Status != old.Status {
		if !validReleaseStatuses[*ch.Status] {
			return model.Release{}, fmt.Errorf("status 必须为 preparing|testing|released|rolled_back，收到 %q", *ch.Status)
		}
		newStatus = *ch.Status
		sets = append(sets, "status = ?")
		args = append(args, *ch.Status)
		change("status", "update_status", old.Status, *ch.Status)
	}
	if ch.Notes != nil && *ch.Notes != old.Notes {
		sets = append(sets, "notes = ?")
		args = append(args, *ch.Notes)
		change("notes", "update", old.Notes, *ch.Notes)
	}
	if ch.FeishuDocToken != nil && *ch.FeishuDocToken != old.FeishuDocToken {
		sets = append(sets, "feishu_doc_token = ?")
		args = append(args, *ch.FeishuDocToken)
		change("feishu_doc_token", "update", old.FeishuDocToken, *ch.FeishuDocToken)
	}

	// 仅真实流转进入 released 补记 released_at（与状态同点写入）；已 released 的
	// notes/doc-token 写回等无关字段更新不得重置该时刻
	if old.Status != newStatus && newStatus == "released" {
		sets = append(sets, "released_at = ?")
		args = append(args, now)
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now, id)
	if _, err := tx.Exec(`UPDATE releases SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Release{}, fmt.Errorf("update release id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.Release{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Release{}, fmt.Errorf("commit update release id=%d: %w", id, err)
	}
	row := s.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id)
	got, err := scanRelease(row.Scan)
	if err != nil {
		return model.Release{}, fmt.Errorf("get release id=%d: %w", id, err)
	}
	return got, nil
}
