package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// The dashboard and the CLI must not silently erase each other's writes.
func TestSaveRefusesWhenFileChangedOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")

	a, _ := Load(path)
	b, _ := Load(path)
	mustCategory(t, a, "Groceries")
	if err := a.Save(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	mustCategory(t, b, "Rent")
	if err := b.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("second save over a new file = %v, want ErrChangedOnDisk", err)
	}

	// The same holds once the file exists.
	a, _ = Load(path)
	b, _ = Load(path)
	mustCategory(t, a, "Gas")
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	mustCategory(t, b, "Travel")
	if err := b.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("stale save = %v, want ErrChangedOnDisk", err)
	}

	// Saving the same DB twice is fine: its own write is not someone else's.
	if err := a.Save(); err != nil {
		t.Errorf("second save of one DB: %v", err)
	}
	got, _ := Load(path)
	if len(got.Categories) != 2 {
		t.Errorf("file holds %d categories, want 2 (Groceries, Gas)", len(got.Categories))
	}
}

func mustCategory(t *testing.T, db *DB, name string) {
	t.Helper()
	if _, err := AddCategory(db, name, KindExpense); err != nil {
		t.Fatal(err)
	}
}
