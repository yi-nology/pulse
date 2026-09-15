package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validRequirementStatuses 与 schema 的 CHECK 约束保持一致。
var validRequirementStatuses = map[string]bool{
	"proposed": true, "reviewing": true, "accepted": true,
	"in_dev": true, "delivered": true, "rejected": true,
}

// RequirementChanges UpdateRequirement 的增量变更集；nil 指针 = 不修改该字段。
// OwnerID 指向 0 表示清空（落 NULL）。FeishuDocToken 供文档绑定写回（Task 3）。
type RequirementChanges struct {
	Title, Description, Status, Source, FeishuDocToken *string
	OwnerID, Priority                                  *int64
}

const requirementCols = `id, project_id, title, description, status, priority, owner_id,
	source, feishu_doc_token, bitable_record_id, bitable_synced_hash, synced_at, archived,
	created_at, updated_at`

// scanRequirement 从一行结果扫描出 model.Requirement（owner_id 可空，NULL 映射 0）。
func scanRequirement(scan func(dest ...any) error) (model.Requirement, error) {
	var r model.Requirement
	var ownerID sql.NullInt64
	var archived int
	if err := scan(&r.ID, &r.ProjectID, &r.Title, &r.Description, &r.Status, &r.Priority,
		&ownerID, &r.Source, &r.FeishuDocToken, &r.BitableRecordID, &r.BitableSyncedHash,
		&r.SyncedAt, &archived, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return model.Requirement{}, err
	}
	r.OwnerID = ownerID.Int64
	r.Archived = archived != 0
	return r, nil
}

// CreateRequirement 创建需求并落 create 活动（与写入同一事务）；status 留空补 proposed，
// priority 为 0 补缺省 3，status 非法时报错。
func (s *Store) CreateRequirement(r model.Requirement, actor model.Member, behalf *model.Member) (model.Requirement, error) {
	status := r.Status
	if status == "" {
		status = "proposed"
	}
	if !validRequirementStatuses[status] {
		return model.Requirement{}, fmt.Errorf("status 必须为 proposed|reviewing|accepted|in_dev|delivered|rejected，收到 %q", status)
	}
	priority := r.Priority
	if priority == 0 {
		priority = 3
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Requirement{}, fmt.Errorf("begin create requirement: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO requirements
		(project_id, title, description, status, priority, owner_id, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Title, r.Description, status, priority, nullID(r.OwnerID), r.Source)
	if err != nil {
		return model.Requirement{}, fmt.Errorf("insert requirement %q: %w", r.Title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Requirement{}, fmt.Errorf("insert requirement %q: %w", r.Title, err)
	}
	if err := insertActivity(tx, entityActivity("requirement", r.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Requirement{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Requirement{}, fmt.Errorf("commit create requirement: %w", err)
	}
	created, _, err := s.GetRequirement(id)
	return created, err
}

// GetRequirement 按 ID 查询需求；不存在时 found=false 且无错误。
func (s *Store) GetRequirement(id int64) (model.Requirement, bool, error) {
	row := s.db.QueryRow(`SELECT `+requirementCols+` FROM requirements WHERE id = ?`, id)
	r, err := scanRequirement(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Requirement{}, false, nil
	}
	if err != nil {
		return model.Requirement{}, false, fmt.Errorf("get requirement id=%d: %w", id, err)
	}
	return r, true, nil
}

// ListRequirements 按项目列出需求（按 id 升序）；status 非空时按状态过滤。
func (s *Store) ListRequirements(projectID int64, status string) ([]model.Requirement, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	rows, err := s.db.Query(`SELECT `+requirementCols+` FROM requirements WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	defer rows.Close()
	var rs []model.Requirement
	for rows.Next() {
		r, err := scanRequirement(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// UpdateRequirement 在单个事务内完成需求更新（与 UpdateTask 同构）：读旧行 → 逐字段
// 比对 → 拼 UPDATE → 逐字段落活动 → 提交。
//   - status 变更记 action="update_status"（需求无 reopen 规则）；
//   - 其他字段变更记 action="update"；
//   - 任何变更刷新 updated_at；无变更（含同值写入）为 no-op，不刷新、不落活动。
func (s *Store) UpdateRequirement(id int64, ch RequirementChanges, actor model.Member, behalf *model.Member) (model.Requirement, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Requirement{}, fmt.Errorf("begin update requirement: %w", err)
	}
	defer tx.Rollback()
	old, err := scanRequirement(tx.QueryRow(`SELECT `+requirementCols+` FROM requirements WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Requirement{}, fmt.Errorf("需求不存在: id=%d", id)
	}
	if err != nil {
		return model.Requirement{}, fmt.Errorf("load requirement id=%d: %w", id, err)
	}

	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, entityActivity("requirement", old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
	}

	if ch.Title != nil && *ch.Title != old.Title {
		sets = append(sets, "title = ?")
		args = append(args, *ch.Title)
		change("title", "update", old.Title, *ch.Title)
	}
	if ch.Description != nil && *ch.Description != old.Description {
		sets = append(sets, "description = ?")
		args = append(args, *ch.Description)
		change("description", "update", old.Description, *ch.Description)
	}
	if ch.Status != nil && *ch.Status != old.Status {
		if !validRequirementStatuses[*ch.Status] {
			return model.Requirement{}, fmt.Errorf("status 必须为 proposed|reviewing|accepted|in_dev|delivered|rejected，收到 %q", *ch.Status)
		}
		sets = append(sets, "status = ?")
		args = append(args, *ch.Status)
		change("status", "update_status", old.Status, *ch.Status)
	}
	if ch.OwnerID != nil && *ch.OwnerID != old.OwnerID {
		sets = append(sets, "owner_id = ?")
		args = append(args, nullID(*ch.OwnerID))
		change("owner_id", "update", old.OwnerID, *ch.OwnerID)
	}
	if ch.Priority != nil && *ch.Priority != int64(old.Priority) {
		sets = append(sets, "priority = ?")
		args = append(args, *ch.Priority)
		change("priority", "update", old.Priority, *ch.Priority)
	}
	if ch.Source != nil && *ch.Source != old.Source {
		sets = append(sets, "source = ?")
		args = append(args, *ch.Source)
		change("source", "update", old.Source, *ch.Source)
	}
	if ch.FeishuDocToken != nil && *ch.FeishuDocToken != old.FeishuDocToken {
		sets = append(sets, "feishu_doc_token = ?")
		args = append(args, *ch.FeishuDocToken)
		change("feishu_doc_token", "update", old.FeishuDocToken, *ch.FeishuDocToken)
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(activitiesLayout), id)
	if _, err := tx.Exec(`UPDATE requirements SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Requirement{}, fmt.Errorf("update requirement id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.Requirement{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Requirement{}, fmt.Errorf("commit update requirement id=%d: %w", id, err)
	}
	r, _, err := s.GetRequirement(id)
	return r, err
}
