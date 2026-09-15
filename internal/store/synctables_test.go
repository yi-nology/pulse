package store

// sync 扩展（v1.1 Task 5）的 store 侧支撑测试：projects.feishu_tables_json 的类型化读写、
// 六实体的 MarkSynced*（含 synced_at 戳）与 SoftDeleteSyncEntity（pull 墓碑归档）。

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

func TestSaveAndGetFeishuTablesRoundTrip(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)

	// 未配置（旧项目）：零值且不报错
	got, err := s.GetFeishuTables(p.ID)
	if err != nil || got != (FeishuTables{}) {
		t.Fatalf("legacy project should yield zero tables: %+v err=%v", got, err)
	}

	want := FeishuTables{Requirements: "tblR", Reviews: "tblV", Meetings: "tblM",
		Bugs: "tblB", TestSubmissions: "tblS", Releases: "tblL"}
	if err := s.SaveFeishuTables(p.ID, want); err != nil {
		t.Fatalf("SaveFeishuTables: %v", err)
	}
	got, err = s.GetFeishuTables(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	// 不影响既有 token 列（向后兼容）
	proj, _, err := s.GetProjectByKey(p.Key)
	if err != nil {
		t.Fatal(err)
	}
	if proj.FeishuBitableAppToken != p.FeishuBitableAppToken || proj.FeishuTaskTableID != p.FeishuTaskTableID {
		t.Fatalf("feishu_tables_json 写入不得影响既有列: %+v", proj)
	}
}

func TestGetFeishuTablesMissingProject(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetFeishuTables(999); err == nil {
		t.Fatal("missing project must error")
	}
	if err := s.SaveFeishuTables(999, FeishuTables{Bugs: "x"}); err == nil {
		t.Fatal("save for missing project must error")
	}
}

func TestMarkSyncedRecordStampsNewEntities(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "导出报表"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSyncedRecord("requirement", r.ID, "recR1", "abcd1234abcd1234"); err != nil {
		t.Fatalf("MarkSyncedRecord(requirement): %v", err)
	}
	got, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BitableRecordID != "recR1" || got.BitableSyncedHash != "abcd1234abcd1234" {
		t.Fatalf("meta not backfilled: %+v", got)
	}
	if got.SyncedAt == "" {
		t.Fatal("六实体必须随 mark synced 刷新 synced_at（覆盖警告的判定基准）")
	}
	if _, err := time.Parse(activitiesLayout, got.SyncedAt); err != nil {
		t.Fatalf("synced_at 格式不符: %q err=%v", got.SyncedAt, err)
	}
	// 未知实体仍报错
	if err := s.MarkSyncedRecord("nope", r.ID, "rec", "hash"); err == nil {
		t.Fatal("unknown entity must error")
	}
}

func TestMarkSyncedHashStampsNewEntities(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	b, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "崩溃"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSyncedHash("bug", b.ID, "hash123hash1234"); err != nil {
		t.Fatalf("MarkSyncedHash(bug): %v", err)
	}
	got, _, err := s.GetBug(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BitableSyncedHash != "hash123hash1234" || got.SyncedAt == "" {
		t.Fatalf("hash/synced_at 未落: %+v", got)
	}
}

func TestSoftDeleteSyncEntityArchivesAndLogsActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "要删的需求"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDeleteSyncEntity("requirement", r.ID, actor, nil); err != nil {
		t.Fatalf("SoftDeleteSyncEntity: %v", err)
	}
	got, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Archived {
		t.Fatalf("archived 未置位: %+v", got)
	}
	acts := recentActivities(t, s, p.ID)
	var found bool
	for _, a := range acts {
		if a.Action == "archive" && a.EntityType == "requirement" && a.EntityID == r.ID {
			found = true
			if !strings.Contains(a.Detail, "archived") {
				t.Fatalf("archive 活动 detail 不符: %q", a.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("archive 活动未落: %+v", acts)
	}
	// 幂等：已归档再删不报错、不重复落活动
	if err := s.SoftDeleteSyncEntity("requirement", r.ID, actor, nil); err != nil {
		t.Fatalf("幂等删除不应报错: %v", err)
	}
	acts = recentActivities(t, s, p.ID)
	if len(acts) != 2 { // create + archive
		t.Fatalf("幂等删除不得重复落活动: %d", len(acts))
	}
}

func TestSoftDeleteSyncEntityValidation(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r, _ := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "x"}, actor, nil)
	if err := s.SoftDeleteSyncEntity("nope", r.ID, actor, nil); err == nil {
		t.Fatal("unknown entity must error")
	}
	if err := s.SoftDeleteSyncEntity("requirement", 999, actor, nil); err == nil {
		t.Fatal("missing row must error")
	}
	// 版本无软删语义
	if err := s.SoftDeleteSyncEntity("version", 1, actor, nil); err == nil {
		t.Fatal("version has no soft-delete semantics, must error")
	}
}
