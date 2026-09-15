package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

// nullID 把 0 映射为 SQL NULL（schema 中 project_id / on_behalf_of 可空，0 表示"无"）。
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// execer 抽象 *sql.DB 与 *sql.Tx 共有的 Exec 能力，使活动写入可在事务内复用。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertActivity 向 activity 表写入一条记录；a.CreatedAt 留空则由 DB 默认值（UTC 文本）填充。
func insertActivity(db execer, a model.Activity) error {
	var err error
	if a.CreatedAt == "" {
		_, err = db.Exec(`INSERT INTO activity
			(project_id, actor_id, actor_type, on_behalf_of, action, entity_type, entity_id, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			nullID(a.ProjectID), a.ActorID, a.ActorType, nullID(a.OnBehalfOf),
			a.Action, a.EntityType, a.EntityID, a.Detail)
	} else {
		_, err = db.Exec(`INSERT INTO activity
			(project_id, actor_id, actor_type, on_behalf_of, action, entity_type, entity_id, detail, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullID(a.ProjectID), a.ActorID, a.ActorType, nullID(a.OnBehalfOf),
			a.Action, a.EntityType, a.EntityID, a.Detail, a.CreatedAt)
	}
	if err != nil {
		return fmt.Errorf("log activity: %w", err)
	}
	return nil
}

// LogActivity 写入一条活动记录（非事务入口，供 CLI 等直接调用）。
func (s *Store) LogActivity(a model.Activity) error {
	return insertActivity(s.db, a)
}

// entityActivity 组装一条通用实体的活动记录，供 v1.1 六实体（requirement/review/
// meeting/bug/test_submission/release）复用；entityType 即 activity.entity_type。
func entityActivity(entityType string, projectID, entityID int64, actor model.Member, behalf *model.Member, action, detail string) model.Activity {
	var onBehalfOf int64
	if behalf != nil {
		onBehalfOf = behalf.ID
	}
	return model.Activity{
		ProjectID: projectID, ActorID: actor.ID, ActorType: actor.Type,
		OnBehalfOf: onBehalfOf, Action: action, EntityType: entityType,
		EntityID: entityID, Detail: detail,
	}
}

// activitiesLayout 与 schema 默认值 strftime('%Y-%m-%d %H:%M:%S','now') 的 UTC 文本格式一致。
const activitiesLayout = "2006-01-02 15:04:05"

// ActivitiesInWindow 返回某项目 [from, to) 内的活动（含 from、不含 to），按 id 升序。
func (s *Store) ActivitiesInWindow(projectID int64, from, to time.Time) ([]model.Activity, error) {
	rows, err := s.db.Query(`SELECT id, project_id, actor_id, actor_type, on_behalf_of,
		action, entity_type, entity_id, detail, created_at
		FROM activity
		WHERE project_id = ? AND created_at >= ? AND created_at < ?
		ORDER BY id`,
		projectID, from.UTC().Format(activitiesLayout), to.UTC().Format(activitiesLayout))
	if err != nil {
		return nil, fmt.Errorf("activities in window: %w", err)
	}
	defer rows.Close()
	var acts []model.Activity
	for rows.Next() {
		var a model.Activity
		var projectID, onBehalfOf sql.NullInt64
		if err := rows.Scan(&a.ID, &projectID, &a.ActorID, &a.ActorType, &onBehalfOf,
			&a.Action, &a.EntityType, &a.EntityID, &a.Detail, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan activity: %w", err)
		}
		a.ProjectID = projectID.Int64
		a.OnBehalfOf = onBehalfOf.Int64
		acts = append(acts, a)
	}
	return acts, rows.Err()
}
