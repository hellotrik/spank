package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatingDailyLogger_writeAndRotate(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	l, err := newRotatingDailyLogger(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	l.setNow(func() time.Time { return fixed })
	l.WriteLine("hello")
	_ = l.Close()

	p := filepath.Join(dir, "spank-2026-03-10.log")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || string(b) == "" {
		t.Fatalf("expected non-empty log: %q", b)
	}

	l2, err := newRotatingDailyLogger(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	l2.setNow(func() time.Time { return fixed.Add(25 * time.Hour) })
	l2.WriteLine("nextday")
	_ = l2.Close()

	if _, err := os.Stat(filepath.Join(dir, "spank-2026-03-11.log")); err != nil {
		t.Fatalf("expected second day file: %v", err)
	}
}

func TestRotatingDailyLogger_prune(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "spank-2026-01-01.log")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "spank-2026-03-25.log")
	if err := os.WriteFile(keep, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newRotatingDailyLogger(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	l.setNow(func() time.Time {
		return time.Date(2026, 3, 28, 10, 0, 0, 0, time.Local)
	})
	l.Prune()
	_ = l.Close()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old log should be removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("recent log should remain: %v", err)
	}
}
