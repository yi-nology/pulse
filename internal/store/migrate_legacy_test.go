package store

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// migrate_legacy_test.go：真实老库升级路径的回归（v1.2 审查修复）。
// 根因：idx_requirements_uid 曾写在 schema 常量里（migrate 主执行），而
// ALTER TABLE requirements ADD COLUMN uid 在其后的 Go 分支才执行——老库
// （v1.1 时代、requirements 无 uid 列）在加列前建索引直接失败
//（"migrate: SQL logic error: no such column: uid"），pulse 完全不可用。
// 造旧库的方式：先用当前 Open 建"新库"，再剥掉 v1.2 新增的索引/列得到
// v1.1 形态（比手工写旧 DDL 更不易腐化）；随后断言：升级成功、索引存在且
// 生效、旧数据保留可回填、二次 Open 幂等。

var wantUIDHex = regexp.MustCompile(`^[0-9a-f]{32}$`)

// stripV12Features 把一个新库剥成 v1.1 形态：去掉 requirements.uid 及其唯一
// 索引、members 的同步镜像列、projects 的 feishu_tables_json。全部为调用点
// 内联字面量 SQL，无拼接、无变量语句。
func stripV12Features(t *testing.T, db *sql.DB) {
	t.Helper()
	steps := []struct {
		name string
		run  func() error
	}{
		{"drop idx_requirements_uid", func() error {
			_, err := db.Exec(`DROP INDEX IF EXISTS idx_requirements_uid`)
			return err
		}},
		{"drop requirements.uid", func() error {
			_, err := db.Exec(`ALTER TABLE requirements DROP COLUMN uid`)
			return err
		}},
		{"drop members.bitable_record_id", func() error {
			_, err := db.Exec(`ALTER TABLE members DROP COLUMN bitable_record_id`)
			return err
		}},
		{"drop members.bitable_synced_hash", func() error {
			_, err := db.Exec(`ALTER TABLE members DROP COLUMN bitable_synced_hash`)
			return err
		}},
		{"drop projects.feishu_tables_json", func() error {
			_, err := db.Exec(`ALTER TABLE projects DROP COLUMN feishu_tables_json`)
			return err
		}},
	}
	for _, st := range steps {
		if err := st.run(); err != nil {
			t.Fatalf("剥离 %s 失败: %v", st.name, err)
		}
	}
}

// seedLegacyV11DB 先建新库再剥离 v1.2 特性，并塞入旧行。
func seedLegacyV11DB(t *testing.T, path string) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("建新库失败: %v", err)
	}
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject("demo", "演示项目", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRequirement(model.Requirement{
		ProjectID: 1, Title: "老库需求", Status: "proposed", Priority: 3,
	}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRequirement(model.Requirement{
		ProjectID: 1, Title: "老库需求二", Status: "reviewing", Priority: 2,
	}, actor, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stripV12Features(t, db)
}

func TestMigrateLegacyV11Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacyV11DB(t, path)

	// 修复前：migrate 在 schema 阶段建 idx_requirements_uid 报
	// "SQL logic error: no such column: uid"，Open 整体失败
	s, err := Open(path)
	if err != nil {
		t.Fatalf("老库升级必须成功: %v", err)
	}
	defer s.Close()

	// 索引已建
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_requirements_uid'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idx_requirements_uid 应存在: n=%d err=%v", n, err)
	}
	// 唯一索引对非空 uid 生效（旧行先回填再撞）
	uid1, err := s.EnsureRequirementUID(1)
	if err != nil || !wantUIDHex.MatchString(uid1) {
		t.Fatalf("旧行 uid 应可回填: %q err=%v", uid1, err)
	}
	uid2, err := s.EnsureRequirementUID(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE requirements SET uid = ? WHERE id = 2`, uid1); err == nil {
		t.Fatal("重复 uid 应被部分唯一索引拦截")
	}
	if uid1 == uid2 {
		t.Fatalf("回填必须互不相同: %q", uid1)
	}
	// 旧数据保留、新链路可用（CreateRequirement 走新列）
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequirement(model.Requirement{ProjectID: 1, Title: "升级后新建"}, actor, nil)
	if err != nil {
		t.Fatalf("升级后建需求必须可用: %v", err)
	}
	if !wantUIDHex.MatchString(r.UID) {
		t.Fatalf("新行应生成 UID: %q", r.UID)
	}
	rs, err := s.ListRequirements(1, "")
	if err != nil || len(rs) != 3 {
		t.Fatalf("旧数据应保留: %+v err=%v", rs, err)
	}

	// 二次 Open 幂等（duplicate column 容忍 + IF NOT EXISTS 索引）
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("二次 migrate 必须幂等: %v", err)
	}
	defer s2.Close()
	rs2, err := s2.ListRequirements(1, "")
	if err != nil || len(rs2) != 3 || rs2[0].UID != uid1 {
		t.Fatalf("二次升级后数据应原样: %+v err=%v", rs2, err)
	}
}
