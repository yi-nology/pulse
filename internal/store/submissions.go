package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validSubmissionStatuses 与 schema 的 CHECK 约束保持一致。
var validSubmissionStatuses = map[string]bool{
	"draft": true, "submitted": true, "testing": true, "passed": true, "failed": true,
}

// SubmissionChanges UpdateTestSubmission 的增量变更集；nil 指针 = 不修改该字段。
// TestOwnerID 指向 0 表示清空（落 NULL）。FeishuDocToken 供提测单文档写回（Task 3）。
// submitted_at/concluded_at 由 store 依状态流转自动补记，不作为可改字段。
type SubmissionChanges struct {
	Status, Scope, FeishuDocToken *string
	TestOwnerID                   *int64
}

const submissionCols = `id, project_id, version_id, requirement_id, submitted_by,
	test_owner_id, status, scope, feishu_doc_token, submitted_at, concluded_at,
	bitable_record_id, bitable_synced_hash, synced_at, archived, created_at, updated_at`

// scanSubmission 从一行结果扫描出 model.TestSubmission（可空引用列 NULL 映射 0）。
func scanSubmission(scan func(dest ...any) error) (model.TestSubmission, error) {
	var t model.TestSubmission
	var requirementID, testOwnerID sql.NullInt64
	var archived int
	if err := scan(&t.ID, &t.ProjectID, &t.VersionID, &requirementID, &t.SubmittedBy,
		&testOwnerID, &t.Status, &t.Scope, &t.FeishuDocToken, &t.SubmittedAt, &t.ConcludedAt,
		&t.BitableRecordID, &t.BitableSyncedHash, &t.SyncedAt, &archived,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return model.TestSubmission{}, err
	}
	t.RequirementID = requirementID.Int64
	t.TestOwnerID = testOwnerID.Int64
	t.Archived = archived != 0
	return t, nil
}

// stampSubmissionTimestamps 依旧/新状态推导应补记的时间点（返回与旧行值合并后的
// 两列最终值）：
//   - 离开 draft（submitted/testing/passed/failed）→ submitted_at；仅填空（已有则
//     保留），对已提交单的无关字段更新天然无副作用；
//   - 真实流转（oldStatus != newStatus）进入 passed/failed → concluded_at。若不看
//     流转只看目标状态，对已定论提测单做 scope/owner/doc-token 等更新会静默重置
//     结论时刻。
func stampSubmissionTimestamps(oldStatus, newStatus, oldSubmittedAt, oldConcludedAt, now string) (submittedAt, concludedAt string) {
	submittedAt, concludedAt = oldSubmittedAt, oldConcludedAt
	if newStatus != "draft" && submittedAt == "" {
		submittedAt = now
	}
	if oldStatus != newStatus && (newStatus == "passed" || newStatus == "failed") {
		concludedAt = now
	}
	return submittedAt, concludedAt
}

// CreateTestSubmission 创建提测单并落 create 活动（与写入同一事务）；status 留空补
// draft，非法报错；submitted_by 留空归当前操作者；非 draft 创建时按状态补记
// submitted_at（passed/failed 再补 concluded_at）。
func (s *Store) CreateTestSubmission(v model.TestSubmission, actor model.Member, behalf *model.Member) (model.TestSubmission, error) {
	status := v.Status
	if status == "" {
		status = "draft"
	}
	if !validSubmissionStatuses[status] {
		return model.TestSubmission{}, fmt.Errorf("status 必须为 draft|submitted|testing|passed|failed，收到 %q", status)
	}
	submittedBy := v.SubmittedBy
	if submittedBy == 0 { // 未显式指定提测人时归当前操作者
		submittedBy = actor.ID
	}
	now := time.Now().UTC().Format(activitiesLayout)
	var submittedAt, concludedAt string
	if status != "draft" { // 非 draft 创建视为已提交，补记提交时刻
		submittedAt = now
	}
	if status == "passed" || status == "failed" {
		concludedAt = now
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("begin create submission: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO test_submissions
		(project_id, version_id, requirement_id, submitted_by, test_owner_id, status,
		 scope, submitted_at, concluded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ProjectID, v.VersionID, nullID(v.RequirementID), submittedBy, nullID(v.TestOwnerID),
		status, v.Scope, submittedAt, concludedAt)
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("insert submission: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("insert submission: %w", err)
	}
	if err := insertActivity(tx, entityActivity("test_submission", v.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.TestSubmission{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TestSubmission{}, fmt.Errorf("commit create submission: %w", err)
	}
	row := s.db.QueryRow(`SELECT `+submissionCols+` FROM test_submissions WHERE id = ?`, id)
	got, err := scanSubmission(row.Scan)
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("get submission id=%d: %w", id, err)
	}
	return got, nil
}

// GetTestSubmission 按 ID 查询提测单；不存在时 found=false 且无错误。
func (s *Store) GetTestSubmission(id int64) (model.TestSubmission, bool, error) {
	row := s.db.QueryRow(`SELECT `+submissionCols+` FROM test_submissions WHERE id = ?`, id)
	t, err := scanSubmission(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TestSubmission{}, false, nil
	}
	if err != nil {
		return model.TestSubmission{}, false, fmt.Errorf("get submission id=%d: %w", id, err)
	}
	return t, true, nil
}

// ListTestSubmissions 按项目列出提测单（按 id 升序）；versionID=0 表示不过滤版本。
func (s *Store) ListTestSubmissions(projectID int64, versionID int64) ([]model.TestSubmission, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if versionID != 0 {
		where = append(where, "version_id = ?")
		args = append(args, versionID)
	}
	rows, err := s.db.Query(`SELECT `+submissionCols+` FROM test_submissions WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	defer rows.Close()
	var ts []model.TestSubmission
	for rows.Next() {
		t, err := scanSubmission(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan submission: %w", err)
		}
		ts = append(ts, t)
	}
	return ts, rows.Err()
}

// UpdateTestSubmission 在单个事务内完成提测单更新（与 UpdateTask 同构）：
//   - status 变更记 action="update_status"；真实流转离开 draft 补记 submitted_at
//     （仅填空），真实流转进入 passed/failed 补记 concluded_at——时间戳为派生值，
//     不单独落活动，且无关字段更新不重置它们；
//   - scope/test_owner/feishu_doc_token 变更记 action="update"；
//   - 任何变更刷新 updated_at；无变更（含同值写入）为 no-op，不刷新、不落活动。
func (s *Store) UpdateTestSubmission(id int64, ch SubmissionChanges, actor model.Member, behalf *model.Member) (model.TestSubmission, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("begin update submission: %w", err)
	}
	defer tx.Rollback()
	old, err := scanSubmission(tx.QueryRow(`SELECT `+submissionCols+` FROM test_submissions WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TestSubmission{}, fmt.Errorf("提测单不存在: id=%d", id)
	}
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("load submission id=%d: %w", id, err)
	}

	now := time.Now().UTC().Format(activitiesLayout)
	var sets []string
	var args []any
	var acts []model.Activity
	change := func(field, action string, from, to any) {
		acts = append(acts, entityActivity("test_submission", old.ProjectID, id, actor, behalf, action, changeDetail(field, from, to)))
	}

	newStatus := old.Status
	if ch.Status != nil && *ch.Status != old.Status {
		if !validSubmissionStatuses[*ch.Status] {
			return model.TestSubmission{}, fmt.Errorf("status 必须为 draft|submitted|testing|passed|failed，收到 %q", *ch.Status)
		}
		newStatus = *ch.Status
		sets = append(sets, "status = ?")
		args = append(args, *ch.Status)
		change("status", "update_status", old.Status, *ch.Status)
	}
	if ch.Scope != nil && *ch.Scope != old.Scope {
		sets = append(sets, "scope = ?")
		args = append(args, *ch.Scope)
		change("scope", "update", old.Scope, *ch.Scope)
	}
	if ch.TestOwnerID != nil && *ch.TestOwnerID != old.TestOwnerID {
		sets = append(sets, "test_owner_id = ?")
		args = append(args, nullID(*ch.TestOwnerID))
		change("test_owner_id", "update", old.TestOwnerID, *ch.TestOwnerID)
	}
	if ch.FeishuDocToken != nil && *ch.FeishuDocToken != old.FeishuDocToken {
		sets = append(sets, "feishu_doc_token = ?")
		args = append(args, *ch.FeishuDocToken)
		change("feishu_doc_token", "update", old.FeishuDocToken, *ch.FeishuDocToken)
	}

	// 状态流转的派生时间戳（submitted_at/concluded_at），与状态同点写入；
	// concluded_at 仅在真实流转进入 passed/failed 时补记
	if len(sets) > 0 {
		submittedAt, concludedAt := stampSubmissionTimestamps(old.Status, newStatus, old.SubmittedAt, old.ConcludedAt, now)
		if submittedAt != old.SubmittedAt {
			sets = append(sets, "submitted_at = ?")
			args = append(args, submittedAt)
		}
		if concludedAt != old.ConcludedAt {
			sets = append(sets, "concluded_at = ?")
			args = append(args, concludedAt)
		}
	}

	if len(sets) == 0 { // 无实际变更
		return old, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now, id)
	if _, err := tx.Exec(`UPDATE test_submissions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.TestSubmission{}, fmt.Errorf("update submission id=%d: %w", id, err)
	}
	for _, a := range acts {
		if err := insertActivity(tx, a); err != nil {
			return model.TestSubmission{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.TestSubmission{}, fmt.Errorf("commit update submission id=%d: %w", id, err)
	}
	row := s.db.QueryRow(`SELECT `+submissionCols+` FROM test_submissions WHERE id = ?`, id)
	got, err := scanSubmission(row.Scan)
	if err != nil {
		return model.TestSubmission{}, fmt.Errorf("get submission id=%d: %w", id, err)
	}
	return got, nil
}
