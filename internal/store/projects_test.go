package store

import (
	"errors"
	"testing"
)

func TestCreateAndGetProject(t *testing.T) {
	s := openTest(t)
	p, err := s.CreateProject("demo", "演示项目", "第一个项目")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || p.Key != "demo" || p.Name != "演示项目" || p.Description != "第一个项目" {
		t.Fatalf("unexpected project: %+v", p)
	}
	if p.Status != "active" {
		t.Fatalf("new project status must be active, got %q", p.Status)
	}

	got, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("GetProjectByKey: found=%v err=%v", found, err)
	}
	if got.ID != p.ID || got.Name != "演示项目" {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, p)
	}

	if _, found, err := s.GetProjectByKey("missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v", found, err)
	}
}

func TestCreateProjectDuplicateKey(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateProject("demo", "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject("demo", "b", ""); !errors.Is(err, ErrDuplicateProject) {
		t.Fatalf("duplicate key must return ErrDuplicateProject, got %v", err)
	}
}

func TestListProjects(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateProject("p1", "一", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject("p2", "二", ""); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Key != "p1" || ps[1].Key != "p2" {
		t.Fatalf("unexpected list: %+v", ps)
	}
}

func TestSaveProjectRoundTrip(t *testing.T) {
	s := openTest(t)
	p, err := s.CreateProject("demo", "演示", "")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "改名"
	p.Description = "新描述"
	p.Status = "archived"
	p.FeishuDocToken = "doc123"
	p.FeishuBitableAppToken = "base456"
	p.FeishuTaskTableID = "tbl_task"
	p.FeishuVersionTableID = "tbl_ver"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("GetProjectByKey: found=%v err=%v", found, err)
	}
	if got != p {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	p.ID = 999 // 不存在的项目
	if err := s.SaveProject(p); err == nil {
		t.Fatal("saving nonexistent project must error")
	}
}
