package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
)

// GetMemberByName 按名字精确查找成员；不存在时返回 (零值, false, nil)。
func (s *Store) GetMemberByName(name string) (model.Member, bool, error) {
	var m model.Member
	err := s.db.QueryRow(
		`SELECT id, name, type, capacity_days_per_week, notes, created_at, bitable_record_id, bitable_synced_hash FROM members WHERE name = ?`,
		name,
	).Scan(&m.ID, &m.Name, &m.Type, &m.Capacity, &m.Notes, &m.CreatedAt, &m.BitableRecordID, &m.BitableSyncedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Member{}, false, nil
	}
	if err != nil {
		return model.Member{}, false, fmt.Errorf("select member %q: %w", name, err)
	}
	return m, true, nil
}

// GetOrCreateMember 按名字取回成员，不存在则创建；同名存在但 type 不一致时报错。
func (s *Store) GetOrCreateMember(name, typ string) (model.Member, error) {
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO members (name, type) VALUES (?, ?)`, name, typ); err != nil {
		return model.Member{}, fmt.Errorf("insert member %q: %w", name, err)
	}
	var m model.Member
	err := s.db.QueryRow(
		`SELECT id, name, type, capacity_days_per_week, notes, created_at, bitable_record_id, bitable_synced_hash FROM members WHERE name = ?`,
		name,
	).Scan(&m.ID, &m.Name, &m.Type, &m.Capacity, &m.Notes, &m.CreatedAt, &m.BitableRecordID, &m.BitableSyncedHash)
	if err != nil {
		return model.Member{}, fmt.Errorf("select member %q: %w", name, err)
	}
	if m.Type != typ {
		return model.Member{}, fmt.Errorf("member %q 已存在且类型为 %s，与请求的 %s 冲突", name, m.Type, typ)
	}
	return m, nil
}

// SetMemberCapacity 更新成员每周可投入人日；成员不存在时报错。
func (s *Store) SetMemberCapacity(id int64, capacity float64) error {
	res, err := s.db.Exec(`UPDATE members SET capacity_days_per_week = ? WHERE id = ?`, capacity, id)
	if err != nil {
		return fmt.Errorf("set member capacity id=%d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("set member capacity: 成员不存在 id=%d", id)
	}
	return nil
}

// ListMembers 返回全部成员（按 id 升序）。
func (s *Store) ListMembers() ([]model.Member, error) {
	rows, err := s.db.Query(
		`SELECT id, name, type, capacity_days_per_week, notes, created_at, bitable_record_id, bitable_synced_hash FROM members ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var ms []model.Member
	for rows.Next() {
		var m model.Member
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.Capacity, &m.Notes, &m.CreatedAt, &m.BitableRecordID, &m.BitableSyncedHash); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}
