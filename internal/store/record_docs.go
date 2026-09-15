package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// recordDocEntity 把实体名映射到 表名 / activity.entity_type（协作记录文档 token 写回用）。
// 五实体均含 feishu_doc_token 列；bug 无内置模板（v1.1 Task 3），不在支持之列。
func recordDocEntity(entity string) (table, entityType string, err error) {
	switch entity {
	case "requirement":
		return "requirements", "requirement", nil
	case "review":
		return "reviews", "review", nil
	case "meeting":
		return "meetings", "meeting", nil
	case "test_submission":
		return "test_submissions", "test_submission", nil
	case "release":
		return "releases", "release", nil
	default:
		return "", "", fmt.Errorf("未知实体 %q（须为 requirement|review|meeting|test_submission|release）", entity)
	}
}

// recordDocLabel 实体的中文名（错误信息用，与各实体文件错误文案一致）。
func recordDocLabel(entityType string) string {
	switch entityType {
	case "requirement":
		return "需求"
	case "review":
		return "评审"
	case "meeting":
		return "会议"
	case "test_submission":
		return "提测单"
	case "release":
		return "发版"
	}
	return entityType
}

// SetRecordDocToken 回写协作记录实体（需求/评审/会议/提测单/发版）的 feishu_doc_token，
// 供飞书 adapter 建完模板文档后调用（协作记录文档只建一次，spec §3.2 内容不回流）。
// 单事务内完成：读旧行（不存在报错）→ token 同值为 no-op（不刷新 updated_at、不落活动）
// → 更新 feishu_doc_token（同点刷新 updated_at）→ 落 activity action="feishu_record_doc"。
func (s *Store) SetRecordDocToken(entity string, id int64, docToken string, actor model.Member, behalf *model.Member) error {
	table, entityType, err := recordDocEntity(entity)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin set %s doc token: %w", entityType, err)
	}
	defer tx.Rollback()
	var projectID int64
	var old string
	err = tx.QueryRow(`SELECT project_id, feishu_doc_token FROM `+table+` WHERE id = ?`, id).
		Scan(&projectID, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s不存在: id=%d", recordDocLabel(entityType), id)
	}
	if err != nil {
		return fmt.Errorf("load %s id=%d: %w", entityType, id, err)
	}
	if old == docToken {
		return nil
	}
	if _, err := tx.Exec(`UPDATE `+table+` SET feishu_doc_token = ?, updated_at = ? WHERE id = ?`,
		docToken, time.Now().UTC().Format(activitiesLayout), id); err != nil {
		return fmt.Errorf("update %s doc token id=%d: %w", entityType, id, err)
	}
	if err := insertActivity(tx, entityActivity(entityType, projectID, id, actor, behalf,
		"feishu_record_doc", changeDetail("feishu_doc_token", old, docToken))); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set %s doc token id=%d: %w", entityType, id, err)
	}
	return nil
}

// SetReviewDocToken 回写评审纪要的 feishu_doc_token（review 无 Update* token 通道的专用入口）。
func (s *Store) SetReviewDocToken(id int64, docToken string, actor model.Member, behalf *model.Member) error {
	return s.SetRecordDocToken("review", id, docToken, actor, behalf)
}

// SetMeetingDocToken 回写会议纪要的 feishu_doc_token（meeting 无 Update* token 通道的专用入口）。
func (s *Store) SetMeetingDocToken(id int64, docToken string, actor model.Member, behalf *model.Member) error {
	return s.SetRecordDocToken("meeting", id, docToken, actor, behalf)
}
