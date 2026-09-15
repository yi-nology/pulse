package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// validReviewKinds / validReviewConclusions 与 plan 约定及 schema 的 CHECK 约束保持一致。
var validReviewKinds = map[string]bool{
	"requirement": true, "release": true, "test": true,
}
var validReviewConclusions = map[string]bool{
	"pending": true, "passed": true, "passed_with_notes": true, "rejected": true,
}

const reviewCols = `id, project_id, requirement_id, kind, held_at, conclusion,
	feishu_doc_token, created_by, bitable_record_id, bitable_synced_hash, synced_at,
	archived, created_at, updated_at`

// scanReview 从一行结果扫描出 model.Review（requirement_id 可空，NULL 映射 0）。
func scanReview(scan func(dest ...any) error) (model.Review, error) {
	var v model.Review
	var requirementID sql.NullInt64
	var archived int
	if err := scan(&v.ID, &v.ProjectID, &requirementID, &v.Kind, &v.HeldAt, &v.Conclusion,
		&v.FeishuDocToken, &v.CreatedBy, &v.BitableRecordID, &v.BitableSyncedHash,
		&v.SyncedAt, &archived, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return model.Review{}, err
	}
	v.RequirementID = requirementID.Int64
	v.Archived = archived != 0
	return v, nil
}

// CreateReview 创建评审记录并落 create 活动（与写入同一事务）；conclusion 留空补
// pending，kind 必须为 requirement|release|test，held_at 留空补当前时刻。
func (s *Store) CreateReview(v model.Review, actor model.Member, behalf *model.Member) (model.Review, error) {
	if !validReviewKinds[v.Kind] {
		return model.Review{}, fmt.Errorf("kind 必须为 requirement|release|test，收到 %q", v.Kind)
	}
	conclusion := v.Conclusion
	if conclusion == "" {
		conclusion = "pending"
	}
	if !validReviewConclusions[conclusion] {
		return model.Review{}, fmt.Errorf("conclusion 必须为 pending|passed|passed_with_notes|rejected，收到 %q", conclusion)
	}
	heldAt := v.HeldAt
	if heldAt == "" {
		heldAt = time.Now().UTC().Format(activitiesLayout)
	}
	createdBy := v.CreatedBy
	if createdBy == 0 { // 未显式指定创建人时归当前操作者
		createdBy = actor.ID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Review{}, fmt.Errorf("begin create review: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO reviews
		(project_id, requirement_id, kind, held_at, conclusion, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.ProjectID, nullID(v.RequirementID), v.Kind, heldAt, conclusion, createdBy)
	if err != nil {
		return model.Review{}, fmt.Errorf("insert review: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Review{}, fmt.Errorf("insert review: %w", err)
	}
	if err := insertActivity(tx, entityActivity("review", v.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Review{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Review{}, fmt.Errorf("commit create review: %w", err)
	}
	return s.getReview(id)
}

// getReview 内部按 ID 查询评审（不存在时返回 ErrNoRows 包装错误）。
func (s *Store) getReview(id int64) (model.Review, error) {
	row := s.db.QueryRow(`SELECT `+reviewCols+` FROM reviews WHERE id = ?`, id)
	v, err := scanReview(row.Scan)
	if err != nil {
		return model.Review{}, fmt.Errorf("get review id=%d: %w", id, err)
	}
	return v, nil
}

// ListReviews 按项目列出评审（按 id 升序）；requirementID=0 表示不过滤需求。
func (s *Store) ListReviews(projectID int64, requirementID int64) ([]model.Review, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if requirementID != 0 {
		where = append(where, "requirement_id = ?")
		args = append(args, requirementID)
	}
	rows, err := s.db.Query(`SELECT `+reviewCols+` FROM reviews WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	defer rows.Close()
	var vs []model.Review
	for rows.Next() {
		v, err := scanReview(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan review: %w", err)
		}
		vs = append(vs, v)
	}
	return vs, rows.Err()
}

// UpdateReviewConclusion 更新评审结论：同值为 no-op（不落活动）；变更刷新 updated_at
// 并落 action="update"、detail {"field":"conclusion",...}（conclusion 非状态字段，
// 不走 update_status）；非法值在 SQL 之前用 Go 校验拒绝。
func (s *Store) UpdateReviewConclusion(id int64, conclusion string, actor model.Member, behalf *model.Member) (model.Review, error) {
	if !validReviewConclusions[conclusion] {
		return model.Review{}, fmt.Errorf("conclusion 必须为 pending|passed|passed_with_notes|rejected，收到 %q", conclusion)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Review{}, fmt.Errorf("begin update review: %w", err)
	}
	defer tx.Rollback()
	old, err := scanReview(tx.QueryRow(`SELECT `+reviewCols+` FROM reviews WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Review{}, fmt.Errorf("评审不存在: id=%d", id)
	}
	if err != nil {
		return model.Review{}, fmt.Errorf("load review id=%d: %w", id, err)
	}
	if conclusion == old.Conclusion {
		return old, nil
	}
	if _, err := tx.Exec(`UPDATE reviews SET conclusion = ?, updated_at = ? WHERE id = ?`,
		conclusion, time.Now().UTC().Format(activitiesLayout), id); err != nil {
		return model.Review{}, fmt.Errorf("update review id=%d: %w", id, err)
	}
	if err := insertActivity(tx, entityActivity("review", old.ProjectID, id, actor, behalf,
		"update", changeDetail("conclusion", old.Conclusion, conclusion))); err != nil {
		return model.Review{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Review{}, fmt.Errorf("commit update review id=%d: %w", id, err)
	}
	return s.getReview(id)
}
