package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validBugStatuses 与 schema 的 CHECK 约束保持一致。
var validBugStatuses = map[string]bool{
	"open": true, "fixing": true, "fixed": true, "verified": true, "closed": true, "wontfix": true,
}

// BugFilter bug 列表过滤条件；零值字段表示"不过滤"。
type BugFilter struct {
	Status     string // "" = 不过滤
	Severity   int64  // 0 = 不过滤（1..4 = P0..P3）
	AssigneeID int64  // 0 = 不过滤
	VersionID  int64  // 0 = 不过滤（按 found_version_id）
}

// BugChanges UpdateBug 的增量变更集；nil 指针 = 不修改该字段。
// AssigneeID/RequirementID/FoundVersionID/FixTaskID 指向 0 表示清空（落 NULL）。
type BugChanges struct {
	Title, Description, Status                *string
	Severity                                  *int64
	AssigneeID, RequirementID, FoundVersionID *int64
	FixTaskID                                 *int64
}

const bugCols = `id, project_id, title, description, severity, status, reporter_id,
	assignee_id, requirement_id, found_version_id, fix_task_id, feishu_doc_token,
	bitable_record_id, bitable_synced_hash, synced_at, archived, created_at, updated_at`

// scanBug 从一行结果扫描出 model.Bug（可空引用列 NULL 映射 0）。
func scanBug(scan func(dest ...any) error) (model.Bug, error) {
	var b model.Bug
	var assigneeID, requirementID, foundVersionID, fixTaskID sql.NullInt64
	var archived int
	if err := scan(&b.ID, &b.ProjectID, &b.Title, &b.Description, &b.Severity, &b.Status,
		&b.ReporterID, &assigneeID, &requirementID, &foundVersionID, &fixTaskID,
		&b.FeishuDocToken, &b.BitableRecordID, &b.BitableSyncedHash, &b.SyncedAt,
		&archived, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return model.Bug{}, err
	}
	b.AssigneeID = assigneeID.Int64
	b.RequirementID = requirementID.Int64
	b.FoundVersionID = foundVersionID.Int64
	b.FixTaskID = fixTaskID.Int64
	b.Archived = archived != 0
	return b, nil
}

// CreateBug 创建 bug 并落 create 活动（与写入同一事务）；status 留空补 open，
// severity 为 0 补缺省 3（P2），非法报错；reporter 留空归当前操作者。
func (s *Store) CreateBug(b model.Bug, actor model.Member, behalf *model.Member) (model.Bug, error) {
	status := b.Status
	if status == "" {
		status = "open"
	}
	if !validBugStatuses[status] {
		return model.Bug{}, fmt.Errorf("status 必须为 open|fixing|fixed|verified|closed|wontfix，收到 %q", status)
	}
	severity := b.Severity
	if severity == 0 {
		severity = 3
	}
	if severity < 1 || severity > 4 {
		return model.Bug{}, fmt.Errorf("severity 必须为 1..4（P0-P3），收到 %d", severity)
	}
	reporterID := b.ReporterID
	if reporterID == 0 { // 未显式指定报告人时归当前操作者
		reporterID = actor.ID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Bug{}, fmt.Errorf("begin create bug: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO bugs
		(project_id, title, description, severity, status, reporter_id, assignee_id,
		 requirement_id, found_version_id, fix_task_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ProjectID, b.Title, b.Description, severity, status, reporterID,
		nullID(b.AssigneeID), nullID(b.RequirementID), nullID(b.FoundVersionID), nullID(b.FixTaskID))
	if err != nil {
		return model.Bug{}, fmt.Errorf("insert bug %q: %w", b.Title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Bug{}, fmt.Errorf("insert bug %q: %w", b.Title, err)
	}
	if err := insertActivity(tx, entityActivity("bug", b.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Bug{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Bug{}, fmt.Errorf("commit create bug: %w", err)
	}
	created, _, err := s.GetBug(id)
	return created, err
}

// GetBug 按 ID 查询 bug；不存在时 found=false 且无错误。
func (s *Store) GetBug(id int64) (model.Bug, bool, error) {
	row := s.db.QueryRow(`SELECT `+bugCols+` FROM bugs WHERE id = ?`, id)
	b, err := scanBug(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bug{}, false, nil
	}
	if err != nil {
		return model.Bug{}, false, fmt.Errorf("get bug id=%d: %w", id, err)
	}
	return b, true, nil
}

// ListBugs 按项目与过滤条件列出 bug（按 id 升序）。
func (s *Store) ListBugs(projectID int64, f BugFilter) ([]model.Bug, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Severity != 0 {
		where = append(where, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.AssigneeID != 0 {
		where = append(where, "assignee_id = ?")
		args = append(args, f.AssigneeID)
	}
	if f.VersionID != 0 {
		where = append(where, "found_version_id = ?")
		args = append(args, f.VersionID)
	}
	rows, err := s.db.Query(`SELECT `+bugCols+` FROM bugs WHERE `+strings.Join(where, " AND ")+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list bugs: %w", err)
	}
	defer rows.Close()
	var bs []model.Bug
	for rows.Next() {
		b, err := scanBug(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan bug: %w", err)
		}
		bs = append(bs, b)
	}
	return bs, rows.Err()
}

// UpdateBug 在单个事务内完成 bug 更新（与 UpdateTask 同构）：读旧行 → 逐字段比对 →
// 拼 UPDATE → 逐字段落活动 → 提交。
//   - status 变更记 action="update_status"；bug 无 reopen 规则（fixed→非 fixed 同样
//     记 update_status）；
//   - 其他字段变更记 action="update"；
//   - 任何变更刷新 updated_at；无变更（含同值写入）为 no-op，不刷新、不落活动。
func (s *Store) UpdateBug(id int64, ch BugChanges, actor model.Member, behalf *model.Member) (model.Bug, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Bug{}, fmt.Errorf("begin update bug: %w", err)
	}
	defer tx.Rollback()
	old, err := scanBug(tx.QueryRow(`SELECT `+bugCols+` FROM bugs WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bug{}, fmt.Errorf("bug 不存在: id=%d", id)
	}
	if err != nil {
		return model.Bug{}, fmt.Errorf("load bug id=%d: %w", id, err)
	}

	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, entityActivity("bug", old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
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
		if !validBugStatuses[*ch.Status] {
			return model.Bug{}, fmt.Errorf("status 必须为 open|fixing|fixed|verified|closed|wontfix，收到 %q", *ch.Status)
		}
		sets = append(sets, "status = ?")
		args = append(args, *ch.Status)
		change("status", "update_status", old.Status, *ch.Status)
	}
	if ch.Severity != nil && *ch.Severity != int64(old.Severity) {
		if *ch.Severity < 1 || *ch.Severity > 4 {
			return model.Bug{}, fmt.Errorf("severity 必须为 1..4（P0-P3），收到 %d", *ch.Severity)
		}
		sets = append(sets, "severity = ?")
		args = append(args, *ch.Severity)
		change("severity", "update", old.Severity, *ch.Severity)
	}
	if ch.AssigneeID != nil && *ch.AssigneeID != old.AssigneeID {
		sets = append(sets, "assignee_id = ?")
		args = append(args, nullID(*ch.AssigneeID))
		change("assignee_id", "update", old.AssigneeID, *ch.AssigneeID)
	}
	if ch.RequirementID != nil && *ch.RequirementID != old.RequirementID {
		sets = append(sets, "requirement_id = ?")
		args = append(args, nullID(*ch.RequirementID))
		change("requirement_id", "update", old.RequirementID, *ch.RequirementID)
	}
	if ch.FoundVersionID != nil && *ch.FoundVersionID != old.FoundVersionID {
		sets = append(sets, "found_version_id = ?")
		args = append(args, nullID(*ch.FoundVersionID))
		change("found_version_id", "update", old.FoundVersionID, *ch.FoundVersionID)
	}
	if ch.FixTaskID != nil && *ch.FixTaskID != old.FixTaskID {
		sets = append(sets, "fix_task_id = ?")
		args = append(args, nullID(*ch.FixTaskID))
		change("fix_task_id", "update", old.FixTaskID, *ch.FixTaskID)
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(activitiesLayout), id)
	if _, err := tx.Exec(`UPDATE bugs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Bug{}, fmt.Errorf("update bug id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.Bug{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Bug{}, fmt.Errorf("commit update bug id=%d: %w", id, err)
	}
	b, _, err := s.GetBug(id)
	return b, err
}
