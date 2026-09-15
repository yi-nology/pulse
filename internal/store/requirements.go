package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
// UID 供同步 pull 侧采纳远端全局身份（双机各自生成的行随共享记录收敛为同一 UID）。
type RequirementChanges struct {
	Title, Description, Status, Source, FeishuDocToken, UID *string
	OwnerID, Priority                                       *int64
}

const requirementCols = `id, project_id, title, description, status, priority, owner_id,
	source, uid, feishu_doc_token, bitable_record_id, bitable_synced_hash, synced_at, archived,
	created_at, updated_at`

// scanRequirement 从一行结果扫描出 model.Requirement（owner_id 可空，NULL 映射 0）。
func scanRequirement(scan func(dest ...any) error) (model.Requirement, error) {
	var r model.Requirement
	var ownerID sql.NullInt64
	var archived int
	if err := scan(&r.ID, &r.ProjectID, &r.Title, &r.Description, &r.Status, &r.Priority,
		&ownerID, &r.Source, &r.UID, &r.FeishuDocToken, &r.BitableRecordID, &r.BitableSyncedHash,
		&r.SyncedAt, &archived, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return model.Requirement{}, err
	}
	r.OwnerID = ownerID.Int64
	r.Archived = archived != 0
	return r, nil
}

// newRequirementUID 生成需求全局身份：crypto/rand 16 字节的十六进制（32 字符）。
// 随 Bitable 需求表同步，跨机引用（评审/bug/提测 的 需求ID 列）按它解析。
func newRequirementUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random for requirement uid: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateRequirement 创建需求并落 create 活动（与写入同一事务）；status 留空补 proposed，
// priority 为 0 补缺省 3，status 非法时报错。UID 留空时自动生成（pull 合入远端行时
// 携带的 UID 原样保留——全局身份以共享记录为准，重新生成会破坏跨机对齐）。
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
	uid := r.UID
	if uid == "" {
		var err error
		if uid, err = newRequirementUID(); err != nil {
			return model.Requirement{}, fmt.Errorf("生成需求UID: %w", err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Requirement{}, fmt.Errorf("begin create requirement: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO requirements
		(project_id, title, description, status, priority, owner_id, source, uid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Title, r.Description, status, priority, nullID(r.OwnerID), r.Source, uid)
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
	if ch.UID != nil && *ch.UID != old.UID {
		sets = append(sets, "uid = ?")
		args = append(args, *ch.UID)
		change("uid", "update", old.UID, *ch.UID)
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

// GetRequirementIDByUID 在项目内按全局 UID 查需求 id（跨机引用解析用：评审/bug/提测
// 的 Bitable 需求ID 列存的是 UID，pull 侧据此还原本地引用）；不存在时 found=false。
func (s *Store) GetRequirementIDByUID(projectID int64, uid string) (int64, bool, error) {
	if uid == "" {
		return 0, false, nil
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM requirements WHERE project_id = ? AND uid = ?`, projectID, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get requirement by uid %q: %w", uid, err)
	}
	return id, true, nil
}

// EnsureRequirementUID 为旧库行（uid=”，v1.2 升级前创建）一次性回填全局 UID，幂等：
// 已有 UID 原样返回、不重新生成；需求不存在时报错。同步路径在推送/拉取前调用，
// 保证旧行首个同步轮次即带上 UID（唯一索引冲突在随机 128 位下概率可忽略）。
// 元数据回填不落活动（避免 activity 噪音）。
func (s *Store) EnsureRequirementUID(id int64) (string, error) {
	var uid string
	err := s.db.QueryRow(`SELECT uid FROM requirements WHERE id = ?`, id).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("需求不存在: id=%d", id)
	}
	if err != nil {
		return "", fmt.Errorf("load requirement id=%d: %w", id, err)
	}
	if uid != "" {
		return uid, nil
	}
	if uid, err = newRequirementUID(); err != nil {
		return "", fmt.Errorf("生成需求UID: %w", err)
	}
	res, err := s.db.Exec(`UPDATE requirements SET uid = ? WHERE id = ? AND uid = ''`, uid, id)
	if err != nil {
		return "", fmt.Errorf("backfill requirement uid id=%d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// 并发下他人已回填：以库内现值为准
		return s.EnsureRequirementUID(id)
	}
	return uid, nil
}
