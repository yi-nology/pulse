package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

const meetingCols = `id, project_id, title, held_at, feishu_doc_token, created_by,
	bitable_record_id, bitable_synced_hash, synced_at, archived, created_at, updated_at`

// scanMeeting 从一行结果扫描出 model.Meeting。
func scanMeeting(scan func(dest ...any) error) (model.Meeting, error) {
	var m model.Meeting
	var archived int
	if err := scan(&m.ID, &m.ProjectID, &m.Title, &m.HeldAt, &m.FeishuDocToken, &m.CreatedBy,
		&m.BitableRecordID, &m.BitableSyncedHash, &m.SyncedAt, &archived,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		return model.Meeting{}, err
	}
	m.Archived = archived != 0
	return m, nil
}

// CreateMeeting 创建会议记录并落 create 活动（与写入同一事务）；held_at 留空补当前
// 时刻，created_by 留空补操作者（参会人/纪要在飞书文档内协作维护，pulse 不持有）。
func (s *Store) CreateMeeting(m model.Meeting, actor model.Member, behalf *model.Member) (model.Meeting, error) {
	heldAt := m.HeldAt
	if heldAt == "" {
		heldAt = time.Now().UTC().Format(activitiesLayout)
	}
	createdBy := m.CreatedBy
	if createdBy == 0 { // 未显式指定创建人时归当前操作者
		createdBy = actor.ID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Meeting{}, fmt.Errorf("begin create meeting: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO meetings (project_id, title, held_at, created_by)
		VALUES (?, ?, ?, ?)`,
		m.ProjectID, m.Title, heldAt, createdBy)
	if err != nil {
		return model.Meeting{}, fmt.Errorf("insert meeting %q: %w", m.Title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Meeting{}, fmt.Errorf("insert meeting %q: %w", m.Title, err)
	}
	if err := insertActivity(tx, entityActivity("meeting", m.ProjectID, id, actor, behalf, "create", "{}")); err != nil {
		return model.Meeting{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Meeting{}, fmt.Errorf("commit create meeting: %w", err)
	}
	row := s.db.QueryRow(`SELECT `+meetingCols+` FROM meetings WHERE id = ?`, id)
	got, err := scanMeeting(row.Scan)
	if err != nil {
		return model.Meeting{}, fmt.Errorf("get meeting id=%d: %w", id, err)
	}
	return got, nil
}

// GetMeeting 按 ID 查询会议；不存在时 found=false 且无错误。
func (s *Store) GetMeeting(id int64) (model.Meeting, bool, error) {
	row := s.db.QueryRow(`SELECT `+meetingCols+` FROM meetings WHERE id = ?`, id)
	m, err := scanMeeting(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Meeting{}, false, nil
	}
	if err != nil {
		return model.Meeting{}, false, fmt.Errorf("get meeting id=%d: %w", id, err)
	}
	return m, true, nil
}

// ListMeetings 按项目列出会议（按 id 升序）。
func (s *Store) ListMeetings(projectID int64) ([]model.Meeting, error) {
	rows, err := s.db.Query(`SELECT `+meetingCols+` FROM meetings WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list meetings: %w", err)
	}
	defer rows.Close()
	var ms []model.Meeting
	for rows.Next() {
		m, err := scanMeeting(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan meeting: %w", err)
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}
