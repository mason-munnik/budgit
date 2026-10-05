package store

import (
	"bytes"
	"errors"
	"os"
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

// Restore puts old bytes back only while the file is exactly as expected.
func TestRestoreOnlyOverTheExpectedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	db, _ := Load(path)
	mustCategory(t, db, "Groceries")
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	mustCategory(t, db, "Rent")
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	after := db.Fingerprint()
	if !bytes.Equal(after, Hash(mustRead(t, path))) {
		t.Fatal("Fingerprint does not match the file Save wrote")
	}

	// Someone writes after the point we would restore over: refused.
	other, _ := Load(path)
	mustCategory(t, other, "Gas")
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(path, before, after); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("err %v, want ErrChangedOnDisk", err)
	}
	if !bytes.Contains(mustRead(t, path), []byte("Gas")) {
		t.Fatal("refused restore still touched the file")
	}

	// Over the expected file: restored byte for byte.
	if err := Restore(path, before, other.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, path), before) {
		t.Error("restored file differs from the snapshot")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("restored file mode %v, want 0600", fi.Mode().Perm())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
