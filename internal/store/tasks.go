package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validTaskStatuses 与 schema 的 CHECK 约束保持一致。
var validTaskStatuses = map[string]bool{
	"backlog": true, "todo": true, "in_progress": true, "blocked": true, "done": true,
}

// ErrTaskArchived 表示任务已软删（archived=1），更新与建立依赖均被拒绝（errors.Is 判断）：
// 软删行对所有列表/报告不可见，且 Sync 只向远端推送已废弃墓碑——放行编辑只会造成
// 本地假成功、变更静默丢失，因此一律报错。
var ErrTaskArchived = errors.New("任务已删除")

// TaskFilter 任务列表过滤条件；零值字段表示"不过滤"。
type TaskFilter struct {
	AssigneeID      int64  // 0 = 不过滤
	Status          string // "" = 不过滤
	VersionID       int64  // 0 = 不过滤
	OverdueOnly     bool   // 仅 due_date < 今天（UTC）且 status != 'done'
	IncludeArchived bool   // 默认排除软删（archived=1）的任务
}

// TaskChanges UpdateTask 的增量变更集；nil 指针 = 不修改该字段。
// AssigneeID/VersionID 指向 0 表示清空（落 NULL）。
type TaskChanges struct {
	Title, Description, Status, StartDate, DueDate *string
	AssigneeID, VersionID, Priority                *int64
	EstimateDays                                   *float64
}

const taskCols = `id, project_id, title, description, assignee_id, status, priority,
	estimate_days, start_date, due_date, version_id, bitable_record_id, bitable_synced_hash,
	synced_at, archived, status_changed_at, created_at, updated_at`

// scanTask 从一行结果扫描出 model.Task（assignee_id/version_id 为可空列，NULL 映射 0）。
func scanTask(scan func(dest ...any) error) (model.Task, error) {
	var t model.Task
	var assigneeID, versionID sql.NullInt64
	var archived int
	if err := scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &assigneeID, &t.Status,
		&t.Priority, &t.EstimateDays, &t.StartDate, &t.DueDate, &versionID,
		&t.BitableRecordID, &t.BitableSyncedHash, &t.SyncedAt, &archived,
		&t.StatusChangedAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return model.Task{}, err
	}
	t.AssigneeID = assigneeID.Int64
	t.VersionID = versionID.Int64
	t.Archived = archived != 0
	return t, nil
}

// GetTask 按 ID 查询任务（含已软删的）；不存在时 found=false 且无错误。
func (s *Store) GetTask(id int64) (model.Task, bool, error) {
	row := s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, false, nil
	}
	if err != nil {
		return model.Task{}, false, fmt.Errorf("get task id=%d: %w", id, err)
	}
	return t, true, nil
}

// fieldChange 活动明细的标准结构（status/其他字段变更通用）。
type fieldChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// changeDetail 序列化单字段变更明细，如 {"field":"status","from":"done","to":"todo"}。
func changeDetail(field string, from, to any) string {
	b, err := json.Marshal(fieldChange{Field: field, From: from, To: to})
	if err != nil {
		return "{}" // 字段均为可序列化标量，理论不可达
	}
	return string(b)
}

// taskActivity 组装一条 task 实体的活动记录。
func taskActivity(projectID, taskID int64, actor model.Member, behalf *model.Member, action, detail string) model.Activity {
	var onBehalfOf int64
	if behalf != nil {
		onBehalfOf = behalf.ID
	}
	return model.Activity{
		ProjectID: projectID, ActorID: actor.ID, ActorType: actor.Type,
		OnBehalfOf: onBehalfOf, Action: action, EntityType: "task",
		EntityID: taskID, Detail: detail,
	}
}

// CreateTask 创建任务并落 create 活动（与写入同一事务）；status 留空补 todo，
// priority 为 0 补缺省 3，status 非法时报错。
func (s *Store) CreateTask(t model.Task, actor model.Member, behalf *model.Member) (model.Task, error) {
	status := t.Status
	if status == "" {
		status = "todo"
	}
	if !validTaskStatuses[status] {
		return model.Task{}, fmt.Errorf("status 必须为 backlog|todo|in_progress|blocked|done，收到 %q", status)
	}
	priority := t.Priority
	if priority == 0 {
		priority = 3
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Task{}, fmt.Errorf("begin create task: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO tasks
		(project_id, title, description, assignee_id, status, priority, estimate_days, start_date, due_date, version_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ProjectID, t.Title, t.Description, nullID(t.AssigneeID), status, priority,
		t.EstimateDays, t.StartDate, t.DueDate, nullID(t.VersionID))
	if err != nil {
		return model.Task{}, fmt.Errorf("insert task %q: %w", t.Title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Task{}, fmt.Errorf("insert task %q: %w", t.Title, err)
	}
	if err := insertActivity(tx, taskActivity(t.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, fmt.Errorf("commit create task: %w", err)
	}
	created, _, err := s.GetTask(id)
	return created, err
}

// UpdateTask 在单个事务内完成任务更新：读旧行 → 拼 UPDATE → 逐字段落活动 → 提交。
//   - status 变更：done → 其他 记 action="reopen"，其余记 "update_status"，
//     并刷新 status_changed_at；
//   - 其他字段变更记 action="update"；
//   - 任何变更刷新 updated_at；无变更（含同值写入）为 no-op，不刷新、不落活动；
//   - 已软删任务报 ErrTaskArchived 拒绝。
func (s *Store) UpdateTask(id int64, ch TaskChanges, actor model.Member, behalf *model.Member) (model.Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Task{}, fmt.Errorf("begin update task: %w", err)
	}
	defer tx.Rollback()
	old, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, fmt.Errorf("任务不存在: id=%d", id)
	}
	if err != nil {
		return model.Task{}, fmt.Errorf("load task id=%d: %w", id, err)
	}
	if old.Archived {
		return model.Task{}, fmt.Errorf("%w: id=%d", ErrTaskArchived, id)
	}

	now := time.Now().UTC().Format(activitiesLayout)
	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, taskActivity(old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
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
		if !validTaskStatuses[*ch.Status] {
			return model.Task{}, fmt.Errorf("status 必须为 backlog|todo|in_progress|blocked|done，收到 %q", *ch.Status)
		}
		sets = append(sets, "status = ?", "status_changed_at = ?")
		args = append(args, *ch.Status, now)
		action := "update_status"
		if old.Status == "done" {
			action = "reopen" // 核心不变式：done → 非 done 即重开
		}
		change("status", action, old.Status, *ch.Status)
	}
	if ch.StartDate != nil && *ch.StartDate != old.StartDate {
		sets = append(sets, "start_date = ?")
		args = append(args, *ch.StartDate)
		change("start_date", "update", old.StartDate, *ch.StartDate)
	}
	if ch.DueDate != nil && *ch.DueDate != old.DueDate {
		sets = append(sets, "due_date = ?")
		args = append(args, *ch.DueDate)
		change("due_date", "update", old.DueDate, *ch.DueDate)
	}
	if ch.AssigneeID != nil && *ch.AssigneeID != old.AssigneeID {
		sets = append(sets, "assignee_id = ?")
		args = append(args, nullID(*ch.AssigneeID))
		change("assignee_id", "update", old.AssigneeID, *ch.AssigneeID)
	}
	if ch.VersionID != nil && *ch.VersionID != old.VersionID {
		sets = append(sets, "version_id = ?")
		args = append(args, nullID(*ch.VersionID))
		change("version_id", "update", old.VersionID, *ch.VersionID)
	}
	if ch.Priority != nil && *ch.Priority != int64(old.Priority) {
		sets = append(sets, "priority = ?")
		args = append(args, *ch.Priority)
		change("priority", "update", old.Priority, *ch.Priority)
	}
	if ch.EstimateDays != nil && *ch.EstimateDays != old.EstimateDays {
		sets = append(sets, "estimate_days = ?")
		args = append(args, *ch.EstimateDays)
		change("estimate_days", "update", old.EstimateDays, *ch.EstimateDays)
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now, id)
	if _, err := tx.Exec(`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Task{}, fmt.Errorf("update task id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.Task{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, fmt.Errorf("commit update task id=%d: %w", id, err)
	}
	t, _, err := s.GetTask(id)
	return t, err
}

// SoftDeleteTask 软删任务：archived 置 1（行保留）、刷新 updated_at、落 archive 活动；
// 已归档的任务幂等返回（不重复落活动）；任务不存在时报错。
func (s *Store) SoftDeleteTask(id int64, actor model.Member, behalf *model.Member) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin soft delete task: %w", err)
	}
	defer tx.Rollback()
	old, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("任务不存在: id=%d", id)
	}
	if err != nil {
		return fmt.Errorf("load task id=%d: %w", id, err)
	}
	if old.Archived {
		return nil
	}
	if _, err := tx.Exec(`UPDATE tasks SET archived = 1, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(activitiesLayout), id); err != nil {
		return fmt.Errorf("soft delete task id=%d: %w", id, err)
	}
	if err := insertActivity(tx, taskActivity(old.ProjectID, id, actor, behalf,
		"archive", changeDetail("archived", false, true))); err != nil {
		return err
	}
	return tx.Commit()
}

// ListTasks 按项目与过滤条件列出任务（按 id 升序）；默认排除软删任务。
func (s *Store) ListTasks(projectID int64, f TaskFilter) ([]model.Task, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if !f.IncludeArchived {
		where = append(where, "archived = 0")
	}
	if f.AssigneeID != 0 {
		where = append(where, "assignee_id = ?")
		args = append(args, f.AssigneeID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.VersionID != 0 {
		where = append(where, "version_id = ?")
		args = append(args, f.VersionID)
	}
	if f.OverdueOnly {
		// due_date 为空串时按字典序会小于任何日期，须显式排除
		where = append(where, "due_date != '' AND due_date < ? AND status != 'done'")
		args = append(args, time.Now().UTC().Format("2006-01-02"))
	}
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE `+strings.Join(where, " AND ")+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var ts []model.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		ts = append(ts, t)
	}
	return ts, rows.Err()
}

// ResolveVersionID 解析任务上的版本引用：纯数字按版本 ID（须存在且属于本项目），
// 否则按项目内版本名查 versions 表（versions CRUD 由后续任务提供，此前按名查询
// 在空表上会报"版本不存在"，属预期行为）。
func (s *Store) ResolveVersionID(projectID int64, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, nil
	}
	var id int64
	if v, err := strconv.ParseInt(ref, 10, 64); err == nil {
		id = v
	} else {
		err := s.db.QueryRow(`SELECT id FROM versions WHERE project_id = ? AND name = ?`, projectID, ref).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("版本不存在: %s（可先创建版本，或直接使用版本 ID）", ref)
		}
		if err != nil {
			return 0, fmt.Errorf("resolve version %q: %w", ref, err)
		}
	}
	var pid int64
	err := s.db.QueryRow(`SELECT project_id FROM versions WHERE id = ?`, id).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pid != projectID) {
		return 0, fmt.Errorf("版本不存在: %s（项目 %d 下无此版本）", ref, projectID)
	}
	if err != nil {
		return 0, fmt.Errorf("resolve version %q: %w", ref, err)
	}
	return id, nil
}

// VersionNamesByProject 返回项目内 版本ID → 版本名 映射（供列表展示）。
func (s *Store) VersionNamesByProject(projectID int64) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM versions WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}
