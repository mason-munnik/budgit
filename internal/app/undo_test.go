package app

import (
	"os"
	"strings"
	"testing"

	"github.com/mason-munnik/budgit/internal/store"
)

// importSigned previews and imports csvimport's signed.csv, returning the
// dashboard the import answered with.
func importSigned(t *testing.T, a *App) Dashboard {
	t.Helper()
	csv := fixture(t, "signed.csv")
	p, err := a.PreviewImport(ImportRequest{Path: csv})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.ImportCSV(ImportRequest{Path: csv, Fingerprint: p.Fingerprint})
	if err != nil || out.Dashboard == nil {
		t.Fatalf("import: %+v, %v", out, err)
	}
	return *out.Dashboard
}

func TestUndoRestoresTheFileExactly(t *testing.T) {
	a, path := seeded(t)
	before, _ := os.ReadFile(path)

	d := importSigned(t, a)
	if d.Undo == nil || d.Undo.FileName != "statement.csv" || d.Undo.Imported == 0 {
		t.Fatalf("undo info %+v after import", d.Undo)
	}
	// A plain read keeps offering it.
	if d, _ := a.Dashboard(""); d.Undo == nil {
		t.Fatal("undo vanished on an ordinary refresh")
	}

	d, err := a.UndoImport(Request{})
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("file after undo differs from before the import:\n%s", after)
	}
	if d.Undo != nil {
		t.Error("undo still offered after undoing")
	}
	if _, err := a.UndoImport(Request{}); err == nil {
		t.Error("undid the same import twice")
	}
}

// Any later write from the app itself ends the undo, and the bar goes away.
func TestUndoRefusedAfterAppWrite(t *testing.T) {
	a, path := seeded(t)
	importSigned(t, a)
	d, err := a.AddTransaction(Request{Date: "2026-09-30", Category: "Groceries", Amount: "5", Description: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Undo != nil {
		t.Error("undo still offered after a later change")
	}
	if _, err := a.UndoImport(Request{}); err == nil {
		t.Fatal("undo allowed after a later change")
	}
	if db, _ := store.Load(path); !hasDesc(db, "Later") {
		t.Error("the later transaction was lost")
	}
}

// A write from outside the app (the CLI) is caught by the hash: refused, and
// that write survives.
func TestUndoRefusedAfterOutsideWrite(t *testing.T) {
	a, path := seeded(t)
	importSigned(t, a)

	cli, _ := store.Load(path)
	if _, err := store.AddTransaction(cli, "2026-09-30", "Checking", "Groceries", "From the CLI", "7"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Save(); err != nil {
		t.Fatal(err)
	}

	_, err := a.UndoImport(Request{})
	if err == nil || !strings.Contains(err.Error(), "Nothing was undone") {
		t.Fatalf("err %v, want a refusal", err)
	}
	if db, _ := store.Load(path); !hasDesc(db, "From the CLI") {
		t.Error("undo erased the CLI's transaction")
	}
	if d, _ := a.Dashboard(""); d.Undo != nil {
		t.Error("undo still offered after the refusal")
	}
}

func TestUndoWithNothingToUndo(t *testing.T) {
	a, _ := seeded(t)
	if d, _ := a.Dashboard(""); d.Undo != nil {
		t.Error("undo offered before any import")
	}
	if _, err := a.UndoImport(Request{}); err == nil {
		t.Error("undo with nothing to undo succeeded")
	}
}

func hasDesc(db *store.DB, desc string) bool {
	for _, t := range db.Transactions {
		if t.Description == desc {
			return true
		}
	}
	return false
}
