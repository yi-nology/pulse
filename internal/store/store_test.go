package store

import (
	"testing"

	_ "github.com/zhangyi/pulse/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTest(t)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil { // 二次执行不得报错
		t.Fatal(err)
	}
}

func TestOpenBadPathFails(t *testing.T) {
	if _, err := Open("/nonexistent-dir-x/pulse.db"); err == nil {
		t.Fatal("expected error for unwritable path")
	}
}
