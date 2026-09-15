package store

import (
	"regexp"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// requirements_uid_test.go：需求全局身份（UID）的 TDD（v1.2）。
// 本地自增 id 只在本机唯一，双机各自建需求会撞号；UID 是 32 位十六进制的全局身份，
// 随 Bitable 需求表同步，跨机引用（评审/bug/提测 的 需求ID 列）按它解析。

var wantUIDShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

func mustRequirement(t *testing.T, s *Store, projectID int64, actor model.Member, title string) model.Requirement {
	t.Helper()
	r, err := s.CreateRequirement(model.Requirement{ProjectID: projectID, Title: title}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestRequirementUIDGeneratedOnCreate：创建时自动生成 32 位十六进制 UID，稳定唯一，
// 全链路（Get/List/Update）读取一致且更新不变。
func TestRequirementUIDGeneratedOnCreate(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)

	r1 := mustRequirement(t, s, p.ID, actor, "需求一")
	r2 := mustRequirement(t, s, p.ID, actor, "需求二")
	if !wantUIDShape.MatchString(r1.UID) || !wantUIDShape.MatchString(r2.UID) {
		t.Fatalf("UID 应为 32 位十六进制: %q %q", r1.UID, r2.UID)
	}
	if r1.UID == r2.UID {
		t.Fatalf("UID 必须唯一: %q", r1.UID)
	}
	// Get/List 全链路带回
	got, found, err := s.GetRequirement(r1.ID)
	if err != nil || !found || got.UID != r1.UID {
		t.Fatalf("GetRequirement 未带回 UID: %+v found=%v err=%v", got, found, err)
	}
	rs, err := s.ListRequirements(p.ID, "")
	if err != nil || len(rs) != 2 || rs[0].UID != r1.UID || rs[1].UID != r2.UID {
		t.Fatalf("ListRequirements 未带回 UID: %+v err=%v", rs, err)
	}
	// 更新不影响 UID
	newTitle := "改名"
	if _, err := s.UpdateRequirement(r1.ID, RequirementChanges{Title: &newTitle}, actor, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.GetRequirement(r1.ID)
	if after.UID != r1.UID {
		t.Fatalf("更新不应改变 UID: %q → %q", r1.UID, after.UID)
	}
}

// TestCreateRequirementKeepsProvidedUID：pull 合入远端行时 UID 随记录带入，不被重新生成
// （全局身份以共享记录为准，重新生成会破坏跨机对齐）。
func TestCreateRequirementKeepsProvidedUID(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "远端来的", UID: "abc123"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.UID != "abc123" {
		t.Fatalf("已提供 UID 应保留: got %q", r.UID)
	}
}

// TestGetRequirementIDByUID：项目内按 UID 查 id；跨项目/未知 UID 查不到（found=false）。
func TestGetRequirementIDByUID(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r := mustRequirement(t, s, p.ID, actor, "导出报表")

	id, found, err := s.GetRequirementIDByUID(p.ID, r.UID)
	if err != nil || !found || id != r.ID {
		t.Fatalf("GetRequirementIDByUID = (%d,%v,%v), want (%d,true,nil)", id, found, err, r.ID)
	}
	if _, found, err := s.GetRequirementIDByUID(p.ID, "不存在"); err != nil || found {
		t.Fatalf("未知 UID 应 found=false: (%v,%v)", found, err)
	}
	if _, found, err := s.GetRequirementIDByUID(p.ID, ""); err != nil || found {
		t.Fatalf("空 UID 应 found=false: (%v,%v)", found, err)
	}
	other, err := s.CreateProject("other", "其他项目", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetRequirementIDByUID(other.ID, r.UID); found {
		t.Fatal("跨项目 UID 查找不应命中")
	}
}

// TestEnsureRequirementUIDBackfillsLegacyRow：旧库行（uid=”）经 EnsureRequirementUID
// 一次性回填（幂等：已有 UID 原样返回，不重新生成）。
func TestEnsureRequirementUIDBackfillsLegacyRow(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r := mustRequirement(t, s, p.ID, actor, "旧库需求")
	// 模拟旧库：直接清空 uid 列（测试同包可触库）
	if _, err := s.db.Exec(`UPDATE requirements SET uid = '' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}
	uid, err := s.EnsureRequirementUID(r.ID)
	if err != nil {
		t.Fatalf("EnsureRequirementUID: %v", err)
	}
	if !wantUIDShape.MatchString(uid) {
		t.Fatalf("回填 UID 应为 32 位十六进制: %q", uid)
	}
	again, err := s.EnsureRequirementUID(r.ID)
	if err != nil || again != uid {
		t.Fatalf("幂等重入应原样返回: %q vs %q err=%v", again, uid, err)
	}
	got, _, _ := s.GetRequirement(r.ID)
	if got.UID != uid {
		t.Fatalf("回填未落库: %+v", got)
	}
	if _, err := s.EnsureRequirementUID(999); err == nil {
		t.Fatal("需求不存在应报错")
	}
}

// TestRequirementUIDUniqueIndex：部分唯一索引拦截重复 UID（空 uid 行不受限）。
func TestRequirementUIDUniqueIndex(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r1 := mustRequirement(t, s, p.ID, actor, "一")
	r2 := mustRequirement(t, s, p.ID, actor, "二")
	if _, err := s.db.Exec(`UPDATE requirements SET uid = ? WHERE id = ?`, r1.UID, r2.ID); err == nil {
		t.Fatal("重复 UID 应被唯一索引拦截")
	}
	// 空 uid 多行共存（旧行迁移期容忍）
	if _, err := s.db.Exec(`UPDATE requirements SET uid = '' WHERE id IN (?, ?)`, r1.ID, r2.ID); err != nil {
		t.Fatalf("空 uid 不应受唯一索引限制: %v", err)
	}
}
