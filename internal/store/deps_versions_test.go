package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// changeVersionActivities 取近 1 小时内 version 实体的"变更类"活动（排除 create）。
func changeVersionActivities(t *testing.T, s *Store, projectID int64) []model.Activity {
	t.Helper()
	var out []model.Activity
	for _, a := range recentActivities(t, s, projectID) {
		if a.EntityType == "version" && a.Action != "create" {
			out = append(out, a)
		}
	}
	return out
}

func TestAddDependencyAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}
	t1 := seedTask(t, s, p.ID, actor, nil)
	t2 := seedTask(t, s, p.ID, actor, nil)

	if err := s.AddDependency(t1.ID, t2.ID, actor, &boss); err != nil {
		t.Fatal(err)
	}
	deps, err := s.ListDependencies(p.ID)
	if err != nil || len(deps) != 1 {
		t.Fatalf("list deps: %+v err=%v", deps, err)
	}
	if d := deps[0]; d.TaskID != t1.ID || d.DependsOnTaskID != t2.ID || d.Type != "FS" {
		t.Fatalf("unexpected dependency: %+v", d)
	}
	// 活动：2 条 create + 1 条 add_dependency，与写入同事务落库
	acts := recentActivities(t, s, p.ID)
	if len(acts) != 3 {
		t.Fatalf("expected 3 activities (2 create + 1 add_dependency), got %d: %+v", len(acts), acts)
	}
	a := acts[2]
	if a.Action != "add_dependency" || a.EntityType != "task" || a.EntityID != t1.ID ||
		a.ProjectID != p.ID || a.ActorID != actor.ID || a.OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", a)
	}
	want := `{"depends_on_task_id":` + strconv.FormatInt(t2.ID, 10) + `,"type":"FS"}`
	if a.Detail != want {
		t.Fatalf("detail %q must be %q", a.Detail, want)
	}
}

// TestAddDependencyArchivedRejected 已软删任务不能作为依赖的任一方：必须以
// ErrTaskArchived 拒绝（文案「任务已删除: id=N」），不落依赖、不落活动。
func TestAddDependencyArchivedRejected(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	t1 := seedTask(t, s, p.ID, actor, nil)
	t2 := seedTask(t, s, p.ID, actor, nil)
	t3 := seedTask(t, s, p.ID, actor, nil)
	if err := s.SoftDeleteTask(t1.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDeleteTask(t2.ID, actor, nil); err != nil {
		t.Fatal(err)
	}

	// 依赖发起方已归档
	err := s.AddDependency(t1.ID, t3.ID, actor, nil)
	if !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("dep from archived task must be ErrTaskArchived, got %v", err)
	}
	if want := fmt.Sprintf("任务已删除: id=%d", t1.ID); err.Error() != want {
		t.Fatalf("error text %q must be %q", err.Error(), want)
	}
	// 依赖目标已归档
	err = s.AddDependency(t3.ID, t2.ID, actor, nil)
	if !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("dep onto archived task must be ErrTaskArchived, got %v", err)
	}
	if want := fmt.Sprintf("任务已删除: id=%d", t2.ID); err.Error() != want {
		t.Fatalf("error text %q must be %q", err.Error(), want)
	}
	if deps, err := s.ListDependencies(p.ID); err != nil || len(deps) != 0 {
		t.Fatalf("rejected deps must not persist: %+v err=%v", deps, err)
	}
	// 仅两条 archive 活动；被拒的 add_dependency 不得再落
	acts := changeActivities(t, s, p.ID)
	if len(acts) != 2 {
		t.Fatalf("rejected deps must not log activity, got %+v", acts)
	}
}

func TestAddDependencySelfRejected(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	t1 := seedTask(t, s, p.ID, actor, nil)

	err := s.AddDependency(t1.ID, t1.ID, actor, nil)
	if !errors.Is(err, ErrSelfDependency) {
		t.Fatalf("self dependency must be ErrSelfDependency, got %v", err)
	}
	// 不落依赖、不落活动
	if deps, err := s.ListDependencies(p.ID); err != nil || len(deps) != 0 {
		t.Fatalf("rejected dep must not persist: %+v err=%v", deps, err)
	}
	if acts := changeActivities(t, s, p.ID); len(acts) != 0 {
		t.Fatalf("rejected dep must not log activity: %+v", acts)
	}
}

func TestAddDependencyDuplicateRejected(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	t1 := seedTask(t, s, p.ID, actor, nil)
	t2 := seedTask(t, s, p.ID, actor, nil)

	if err := s.AddDependency(t1.ID, t2.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	err := s.AddDependency(t1.ID, t2.ID, actor, nil)
	if !errors.Is(err, ErrDuplicateDependency) {
		t.Fatalf("duplicate dependency must be ErrDuplicateDependency, got %v", err)
	}
	// 唯一索引兜底：绕过 AddDependency 预查询直接插重复对，
	// 必须被 idx_dependencies_pair 拒绝（TOCTOU 防线的存在性验证）。
	if _, err := s.db.Exec(`INSERT INTO dependencies (task_id, depends_on_task_id) VALUES (?, ?)`,
		t1.ID, t2.ID); !isUniqueViolation(err) {
		t.Fatalf("unique index must reject duplicate pair, got %v", err)
	}
	// 反方向是另一条依赖，允许
	if err := s.AddDependency(t2.ID, t1.ID, actor, nil); err != nil {
		t.Fatalf("reverse direction must be allowed: %v", err)
	}
	deps, err := s.ListDependencies(p.ID)
	if err != nil || len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %+v err=%v", deps, err)
	}
}

func TestAddDependencyValidations(t *testing.T) {
	s := openTest(t)
	p1 := seedProject(t, s)
	p2, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	actor := taskActor(t, s)
	a1 := seedTask(t, s, p1.ID, actor, nil)
	b1 := seedTask(t, s, p2.ID, actor, nil)

	// 任务不存在
	if err := s.AddDependency(999, a1.ID, actor, nil); err == nil || !strings.Contains(err.Error(), "任务不存在") {
		t.Fatalf("missing task must error 任务不存在, got %v", err)
	}
	if err := s.AddDependency(a1.ID, 999, actor, nil); err == nil || !strings.Contains(err.Error(), "任务不存在") {
		t.Fatalf("missing depends-on task must error 任务不存在, got %v", err)
	}
	// 跨项目依赖拒绝
	if err := s.AddDependency(a1.ID, b1.ID, actor, nil); err == nil || !strings.Contains(err.Error(), "同一项目") {
		t.Fatalf("cross-project dep must error, got %v", err)
	}
}

func TestListDependenciesScopedToProject(t *testing.T) {
	s := openTest(t)
	p1 := seedProject(t, s)
	p2, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	actor := taskActor(t, s)
	a1 := seedTask(t, s, p1.ID, actor, nil)
	a2 := seedTask(t, s, p1.ID, actor, nil)
	b1 := seedTask(t, s, p2.ID, actor, nil)
	b2 := seedTask(t, s, p2.ID, actor, nil)

	if err := s.AddDependency(a1.ID, a2.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(b1.ID, b2.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 越界行：p2 任务依赖 p1 任务（绕过 API 直接植入），p1 列表不得串项目
	if _, err := s.db.Exec(`INSERT INTO dependencies (task_id, depends_on_task_id) VALUES (?, ?)`, b1.ID, a1.ID); err != nil {
		t.Fatal(err)
	}

	deps, err := s.ListDependencies(p1.ID)
	if err != nil || len(deps) != 1 || deps[0].TaskID != a1.ID || deps[0].DependsOnTaskID != a2.ID {
		t.Fatalf("p1 deps must be scoped to project: %+v err=%v", deps, err)
	}
	deps, err = s.ListDependencies(p2.ID)
	if err != nil || len(deps) != 2 {
		t.Fatalf("p2 deps: %+v err=%v", deps, err)
	}
}

func TestCreateVersionDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}

	v, err := s.CreateVersion(model.Version{
		ProjectID: p.ID, Name: "v1.0", TargetDate: "2026-10-01",
	}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省 status=planned
	if v.ID == 0 || v.Name != "v1.0" || v.Status != "planned" || v.TargetDate != "2026-10-01" {
		t.Fatalf("unexpected version: %+v", v)
	}
	got, found, err := s.GetVersion(v.ID)
	if err != nil || !found {
		t.Fatalf("GetVersion: found=%v err=%v", found, err)
	}
	if got != v {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, v)
	}
	acts := recentActivities(t, s, p.ID)
	if len(acts) != 1 {
		t.Fatalf("create must log exactly 1 activity, got %d", len(acts))
	}
	a := acts[0]
	if a.Action != "create" || a.EntityType != "version" || a.EntityID != v.ID ||
		a.ProjectID != p.ID || a.ActorID != actor.ID || a.OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", a)
	}
}

func TestCreateVersionDuplicateName(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	other, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	actor := taskActor(t, s)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 同项目同名：唯一约束冲突须映射为 ErrDuplicateVersion（中文文案）
	_, err = s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if !errors.Is(err, ErrDuplicateVersion) {
		t.Fatalf("duplicate version name must be ErrDuplicateVersion, got %v", err)
	}
	if !strings.Contains(err.Error(), "版本已存在") {
		t.Fatalf("error must be Chinese 文案, got %q", err.Error())
	}
	// 跨项目同名允许
	if _, err := s.CreateVersion(model.Version{ProjectID: other.ID, Name: "v1.0"}, actor, nil); err != nil {
		t.Fatalf("same name in another project must be allowed: %v", err)
	}
	// 空名拒绝
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: " "}, actor, nil); err == nil {
		t.Fatal("blank version name must error")
	}
}

func TestCreateVersionInvalidStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1", Status: "doing"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestGetVersionAndListVersions(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	other, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	actor := taskActor(t, s)
	v1, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v2.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVersion(model.Version{ProjectID: other.ID, Name: "v1.0"}, actor, nil); err != nil {
		t.Fatal(err)
	}

	// 列表按项目隔离、按 id 升序
	vs, err := s.ListVersions(p.ID)
	if err != nil || len(vs) != 2 || vs[0].ID != v1.ID || vs[1].ID != v2.ID {
		t.Fatalf("list versions: %+v err=%v", vs, err)
	}
	vs, err = s.ListVersions(other.ID)
	if err != nil || len(vs) != 1 {
		t.Fatalf("other project versions: %+v err=%v", vs, err)
	}
	// 不存在时 found=false 且无错误
	if _, found, err := s.GetVersion(999); err != nil || found {
		t.Fatalf("GetVersion(999): found=%v err=%v", found, err)
	}
}

func TestUpdateVersionStatusActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UpdateVersion(v.ID, VersionChanges{Status: strptr("in_dev")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_dev" {
		t.Fatalf("status must be in_dev, got %q", got.Status)
	}
	acts := changeVersionActivities(t, s, p.ID)
	if len(acts) != 1 || acts[0].Action != "update_status" {
		t.Fatalf("status change must log exactly 1 update_status, got %+v", acts)
	}
	if want := `{"field":"status","from":"planned","to":"in_dev"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}

	// shipped → planned：版本无 reopen 语义，仍记 update_status
	if _, err := s.UpdateVersion(v.ID, VersionChanges{Status: strptr("shipped")}, actor, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.UpdateVersion(v.ID, VersionChanges{Status: strptr("planned")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	acts = changeVersionActivities(t, s, p.ID)
	if len(acts) != 3 {
		t.Fatalf("expected 3 status activities, got %d: %+v", len(acts), acts)
	}
	last := acts[2]
	if last.Action != "update_status" {
		t.Fatalf("versions must NOT use reopen semantics, got %q", last.Action)
	}
	if want := `{"field":"status","from":"shipped","to":"planned"}`; last.Detail != want {
		t.Fatalf("detail %q must be %q", last.Detail, want)
	}
	if got.Status != "planned" {
		t.Fatalf("status must be planned, got %q", got.Status)
	}
}

func TestUpdateVersionTargetAndNotes(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UpdateVersion(v.ID, VersionChanges{
		TargetDate: strptr("2026-11-01"), Notes: strptr("顺延一个迭代"),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetDate != "2026-11-01" || got.Notes != "顺延一个迭代" {
		t.Fatalf("fields not updated: %+v", got)
	}
	acts := changeVersionActivities(t, s, p.ID)
	if len(acts) != 2 {
		t.Fatalf("expected 2 update activities, got %d: %+v", len(acts), acts)
	}
	if acts[0].Action != "update" || acts[0].Detail != `{"field":"target_date","from":"","to":"2026-11-01"}` {
		t.Fatalf("unexpected first activity: %+v", acts[0])
	}
	if acts[1].Action != "update" || acts[1].Detail != `{"field":"notes","from":"","to":"顺延一个迭代"}` {
		t.Fatalf("unexpected second activity: %+v", acts[1])
	}
}

func TestUpdateVersionNoop(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 空变更集与同值写入均为 no-op，不落活动
	got, err := s.UpdateVersion(v.ID, VersionChanges{}, actor, nil)
	if err != nil || got != v {
		t.Fatalf("empty changes must be no-op: %+v err=%v", got, err)
	}
	same := v.Status
	if _, err := s.UpdateVersion(v.ID, VersionChanges{Status: &same}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := changeVersionActivities(t, s, p.ID); len(acts) != 0 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}
}

func TestUpdateVersionErrors(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 非法状态
	if _, err := s.UpdateVersion(v.ID, VersionChanges{Status: strptr("doing")}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
	// 不存在
	if _, err := s.UpdateVersion(999, VersionChanges{Notes: strptr("x")}, actor, nil); err == nil ||
		!strings.Contains(err.Error(), "版本不存在") {
		t.Fatalf("missing version must error 版本不存在, got %v", err)
	}
}
