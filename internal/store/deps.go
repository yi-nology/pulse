package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zhangyi/pulse/internal/model"
)

// ErrSelfDependency 表示任务试图依赖自己（errors.Is 判断）。
var ErrSelfDependency = errors.New("任务不能依赖自己")

// ErrDuplicateDependency 表示同向依赖关系已存在（errors.Is 判断）。
var ErrDuplicateDependency = errors.New("依赖关系已存在")

// depDetail add_dependency 活动明细。
type depDetail struct {
	DependsOnTaskID int64  `json:"depends_on_task_id"`
	Type            string `json:"type"`
}

// AddDependency 为任务建立依赖（task 依赖 depends_on），与 add_dependency 活动
// 同事务落库。自依赖、重复依赖、任务不存在、已软删（任一方）、跨项目依赖均报错。
func (s *Store) AddDependency(taskID, dependsOnID int64, actor model.Member, behalf *model.Member) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin add dependency: %w", err)
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, taskID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("任务不存在: id=%d", taskID)
	}
	if err != nil {
		return fmt.Errorf("load task id=%d: %w", taskID, err)
	}
	if task.Archived {
		return fmt.Errorf("%w: id=%d", ErrTaskArchived, taskID)
	}
	if taskID == dependsOnID {
		return fmt.Errorf("%w: id=%d", ErrSelfDependency, taskID)
	}
	dep, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, dependsOnID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("任务不存在: id=%d", dependsOnID)
	}
	if err != nil {
		return fmt.Errorf("load task id=%d: %w", dependsOnID, err)
	}
	if dep.Archived {
		return fmt.Errorf("%w: id=%d", ErrTaskArchived, dependsOnID)
	}
	if task.ProjectID != dep.ProjectID {
		return fmt.Errorf("依赖双方必须属于同一项目: 任务 %d 在项目 %d，任务 %d 在项目 %d",
			taskID, task.ProjectID, dependsOnID, dep.ProjectID)
	}
	var exists int
	err = tx.QueryRow(`SELECT 1 FROM dependencies WHERE task_id = ? AND depends_on_task_id = ?`,
		taskID, dependsOnID).Scan(&exists)
	if err == nil {
		return fmt.Errorf("%w: 任务 %d 已依赖任务 %d", ErrDuplicateDependency, taskID, dependsOnID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check dependency %d->%d: %w", taskID, dependsOnID, err)
	}
	if _, err := tx.Exec(`INSERT INTO dependencies (task_id, depends_on_task_id) VALUES (?, ?)`,
		taskID, dependsOnID); err != nil {
		// 预查询与插入之间存在并发窗口，唯一索引 idx_dependencies_pair 兜底，
		// 冲突同样映射为 ErrDuplicateDependency（与 versions 的处理同构）。
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: 任务 %d 已依赖任务 %d", ErrDuplicateDependency, taskID, dependsOnID)
		}
		return fmt.Errorf("insert dependency %d->%d: %w", taskID, dependsOnID, err)
	}
	detail, err := json.Marshal(depDetail{DependsOnTaskID: dependsOnID, Type: "FS"})
	if err != nil {
		return fmt.Errorf("marshal dependency detail: %w", err)
	}
	if err := insertActivity(tx, taskActivity(task.ProjectID, taskID, actor, behalf,
		"add_dependency", string(detail))); err != nil {
		return err
	}
	return tx.Commit()
}

// ListDependencies 列出项目内全部依赖关系（JOIN tasks 限定项目，按 id 升序）。
func (s *Store) ListDependencies(projectID int64) ([]model.Dependency, error) {
	rows, err := s.db.Query(`
		SELECT d.id, d.task_id, d.depends_on_task_id, d.type
		FROM dependencies d
		JOIN tasks t ON t.id = d.task_id
		WHERE t.project_id = ?
		ORDER BY d.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list dependencies: %w", err)
	}
	defer rows.Close()
	var deps []model.Dependency
	for rows.Next() {
		var d model.Dependency
		if err := rows.Scan(&d.ID, &d.TaskID, &d.DependsOnTaskID, &d.Type); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}
