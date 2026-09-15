package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// FeishuTables 是 v1.1 研发交付闭环六实体的 bitable 表 id 集合，整体落
// projects.feishu_tables_json（一个 JSON 列而非 6 个新列；既有 task/version 表 id
// 保持独立列不动，向后兼容）。零值 = 项目未配置六实体表（旧项目/采用既有 base 的
// bind 模式），sync 据此跳过六实体、只同步任务与版本，不报错。
type FeishuTables struct {
	Requirements    string `json:"requirements"`
	Reviews         string `json:"reviews"`
	Meetings        string `json:"meetings"`
	Bugs            string `json:"bugs"`
	TestSubmissions string `json:"test_submissions"`
	Releases        string `json:"releases"`
}

// GetFeishuTables 读取项目的六实体表 id；值为空（旧项目）返回零值且不报错。
// JSON 损坏属数据异常，原样返回错误（由调用方决定跳过或告警）。
func (s *Store) GetFeishuTables(projectID int64) (FeishuTables, error) {
	var raw string
	err := s.db.QueryRow(`SELECT feishu_tables_json FROM projects WHERE id = ?`, projectID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return FeishuTables{}, fmt.Errorf("get feishu tables: 项目不存在 id=%d", projectID)
	}
	if err != nil {
		return FeishuTables{}, fmt.Errorf("get feishu tables id=%d: %w", projectID, err)
	}
	if strings.TrimSpace(raw) == "" {
		return FeishuTables{}, nil
	}
	var t FeishuTables
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return FeishuTables{}, fmt.Errorf("get feishu tables id=%d: feishu_tables_json 损坏: %w", projectID, err)
	}
	return t, nil
}

// SaveFeishuTables 写回项目的六实体表 id（bind 建完六表后调用）；项目不存在时报错。
// 只更新 feishu_tables_json 单列，不触碰既有 token 列。
func (s *Store) SaveFeishuTables(projectID int64, t FeishuTables) error {
	b, err := json.Marshal(t)
	if err != nil { // 字段均为 string，理论不可达
		return fmt.Errorf("save feishu tables id=%d: %w", projectID, err)
	}
	res, err := s.db.Exec(`UPDATE projects SET feishu_tables_json = ? WHERE id = ?`, string(b), projectID)
	if err != nil {
		return fmt.Errorf("save feishu tables id=%d: %w", projectID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("save feishu tables: 项目不存在 id=%d", projectID)
	}
	return nil
}
