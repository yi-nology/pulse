package store

import (
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
)

// GetOrCreateMember 按名字取回成员，不存在则创建；同名存在但 type 不一致时报错。
func (s *Store) GetOrCreateMember(name, typ string) (model.Member, error) {
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO members (name, type) VALUES (?, ?)`, name, typ); err != nil {
		return model.Member{}, fmt.Errorf("insert member %q: %w", name, err)
	}
	var m model.Member
	err := s.db.QueryRow(
		`SELECT id, name, type, capacity_days_per_week, notes, created_at FROM members WHERE name = ?`,
		name,
	).Scan(&m.ID, &m.Name, &m.Type, &m.Capacity, &m.Notes, &m.CreatedAt)
	if err != nil {
		return model.Member{}, fmt.Errorf("select member %q: %w", name, err)
	}
	if m.Type != typ {
		return model.Member{}, fmt.Errorf("member %q 已存在且类型为 %s，与请求的 %s 冲突", name, m.Type, typ)
	}
	return m, nil
}

// ListMembers 返回全部成员（按 id 升序）。
func (s *Store) ListMembers() ([]model.Member, error) {
	rows, err := s.db.Query(
		`SELECT id, name, type, capacity_days_per_week, notes, created_at FROM members ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var ms []model.Member
	for rows.Next() {
		var m model.Member
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.Capacity, &m.Notes, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}
