package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
)

// ErrDuplicateProject 表示 project.key 唯一约束冲突（errors.Is 判断）。
var ErrDuplicateProject = errors.New("项目已存在")

const projectCols = `id, key, name, description, status,
	feishu_doc_token, feishu_bitable_app_token, feishu_task_table_id, feishu_version_table_id, created_at`

// scanProject 从一行结果扫描出 model.Project。
func scanProject(scan func(dest ...any) error) (model.Project, error) {
	var p model.Project
	err := scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Status,
		&p.FeishuDocToken, &p.FeishuBitableAppToken, &p.FeishuTaskTableID, &p.FeishuVersionTableID, &p.CreatedAt)
	if err != nil {
		return model.Project{}, err
	}
	return p, nil
}

// CreateProject 创建 status=active 的新项目；key 已存在时报 ErrDuplicateProject。
func (s *Store) CreateProject(key, name, desc string) (model.Project, error) {
	if _, found, err := s.GetProjectByKey(key); err != nil {
		return model.Project{}, err
	} else if found {
		return model.Project{}, fmt.Errorf("%w: %s", ErrDuplicateProject, key)
	}
	if _, err := s.db.Exec(
		`INSERT INTO projects (key, name, description) VALUES (?, ?, ?)`, key, name, desc); err != nil {
		return model.Project{}, fmt.Errorf("insert project %q: %w", key, err)
	}
	p, found, err := s.GetProjectByKey(key)
	if err != nil {
		return model.Project{}, err
	}
	if !found {
		return model.Project{}, fmt.Errorf("insert project %q: 写入后不可见", key)
	}
	return p, nil
}

// GetProjectByKey 按 key 查询项目；不存在时 found=false 且无错误。
func (s *Store) GetProjectByKey(key string) (model.Project, bool, error) {
	row := s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE key = ?`, key)
	p, err := scanProject(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Project{}, false, nil
	}
	if err != nil {
		return model.Project{}, false, fmt.Errorf("get project %q: %w", key, err)
	}
	return p, true, nil
}

// ListProjects 返回全部项目（按 id 升序）。
func (s *Store) ListProjects() ([]model.Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var ps []model.Project
	for rows.Next() {
		p, err := scanProject(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// SaveProject 按 ID 全量更新项目（供 feishu bind 等场景写回 token）；项目不存在时报错。
func (s *Store) SaveProject(p model.Project) error {
	res, err := s.db.Exec(`UPDATE projects SET
		key = ?, name = ?, description = ?, status = ?,
		feishu_doc_token = ?, feishu_bitable_app_token = ?, feishu_task_table_id = ?, feishu_version_table_id = ?
		WHERE id = ?`,
		p.Key, p.Name, p.Description, p.Status,
		p.FeishuDocToken, p.FeishuBitableAppToken, p.FeishuTaskTableID, p.FeishuVersionTableID, p.ID)
	if err != nil {
		return fmt.Errorf("save project id=%d: %w", p.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("save project: 项目不存在 id=%d", p.ID)
	}
	return nil
}
