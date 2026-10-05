package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mason-munnik/budgit/internal/store"
)

// fixture copies one of csvimport's test statements somewhere it can be edited.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "csvimport", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return writeCSV(t, string(data))
}

func writeCSV(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "statement.csv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreviewSavesNothingThenImportDoes(t *testing.T) {
	a, path := seeded(t)
	csv := fixture(t, "signed.csv")
	before, _ := os.ReadFile(path)

	p, err := a.PreviewImport(ImportRequest{Path: csv}) // one account: no need to name it
	if err != nil {
		t.Fatal(err)
	}
	if p.Account != "Checking" || p.New == 0 || len(p.Fingerprint) != 64 || p.SignConflict {
		t.Fatalf("preview %+v", p)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("preview wrote to the data file")
	}

	out, err := a.ImportCSV(ImportRequest{Path: csv, Fingerprint: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if out.Changed || out.Imported != p.New || out.Dashboard == nil {
		t.Fatalf("outcome %+v, want %d imported and a dashboard", out, p.New)
	}
	db, _ := store.Load(path)
	if len(db.Transactions) != p.New {
		t.Errorf("%d transactions saved, want %d", len(db.Transactions), p.New)
	}

	// The same file again is recognised by its bank ids and adds nothing.
	again, err := a.PreviewImport(ImportRequest{Path: csv})
	if err != nil {
		t.Fatal(err)
	}
	if again.New != 0 || again.Known != p.New {
		t.Errorf("second preview new=%d known=%d, want 0 and %d", again.New, again.Known, p.New)
	}
}

// A file that changed after its preview is refused, and the answer is a fresh
// preview of what it holds now.
func TestImportRefusesChangedFile(t *testing.T) {
	a, path := seeded(t)
	csv := fixture(t, "signed.csv")
	p, err := a.PreviewImport(ImportRequest{Path: csv})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(csv, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`"A-9","9/23/2026","9/23/2026","Debit","Posted","-5.00000","Coffee","Dining"` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	out, err := a.ImportCSV(ImportRequest{Path: csv, Fingerprint: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || out.Preview == nil || out.Imported != 0 {
		t.Fatalf("outcome %+v, want changed with a fresh preview", out)
	}
	if out.Preview.Fingerprint == p.Fingerprint || out.Preview.New != p.New+1 {
		t.Errorf("fresh preview %+v should see the new row", out.Preview)
	}
	if db, _ := store.Load(path); len(db.Transactions) != 0 {
		t.Errorf("%d transactions saved from a changed file", len(db.Transactions))
	}
	if _, err := a.ImportCSV(ImportRequest{Path: csv}); err != nil {
		t.Fatal(err)
	}
	if db, _ := store.Load(path); len(db.Transactions) != 0 {
		t.Error("an import with no fingerprint at all was let through")
	}
}

// Option 2: a conflict previews the rows both ways; the choice sticks.
func TestSignConflictShowsBothWays(t *testing.T) {
	a, path := seeded(t)
	csv := fixture(t, "inverted.csv")
	p, err := a.PreviewImport(ImportRequest{Path: csv})
	if err != nil {
		t.Fatal(err)
	}
	if !p.SignConflict || len(p.AsIs) == 0 || len(p.AsIs) != len(p.Flipped) {
		t.Fatalf("preview %+v, want a sign conflict with both versions", p)
	}
	for i := range p.AsIs {
		if p.AsIs[i].AmountCents != -p.Flipped[i].AmountCents {
			t.Errorf("row %d: %d vs %d, want opposite signs", i, p.AsIs[i].AmountCents, p.Flipped[i].AmountCents)
		}
	}
	if _, err := a.ImportCSV(ImportRequest{Path: csv, Fingerprint: p.Fingerprint}); err == nil {
		t.Error("imported a conflicted file without a sign choice")
	}

	flip, err := a.PreviewImport(ImportRequest{Path: csv, Sign: "flip"})
	if err != nil || flip.SignConflict {
		t.Fatalf("flip preview %+v, %v", flip, err)
	}
	if _, err := a.ImportCSV(ImportRequest{Path: csv, Sign: "flip", Fingerprint: flip.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	db, _ := store.Load(path)
	for _, tx := range db.Transactions {
		if tx.Description == "Amazon" && tx.AmountCents != -3744 {
			t.Errorf("Amazon saved as %d, want -3744 (a purchase)", tx.AmountCents)
		}
	}
	if _, err := a.PreviewImport(ImportRequest{Path: csv, Sign: "sideways"}); err == nil {
		t.Error("unknown sign choice accepted")
	}
}

func TestImportRefusesHugeAndMissingFiles(t *testing.T) {
	a, _ := seeded(t)
	big := filepath.Join(t.TempDir(), "huge.csv")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxCSVBytes + 1); err != nil { // sparse: no 10 MB write
		t.Fatal(err)
	}
	_ = f.Close()
	for _, p := range []string{big, filepath.Join(t.TempDir(), "gone.csv"), "", t.TempDir()} {
		if _, err := a.PreviewImport(ImportRequest{Path: p}); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestImportNeedsAccountWhenThereAreSeveral(t *testing.T) {
	a, path := seeded(t)
	db, _ := store.Load(path)
	if _, err := store.AddAccount(db, "Amex", "credit", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	csv := fixture(t, "signed.csv")
	if _, err := a.PreviewImport(ImportRequest{Path: csv}); err == nil || !strings.Contains(err.Error(), "account") {
		t.Errorf("err %v, want a request to choose an account", err)
	}
	p, err := a.PreviewImport(ImportRequest{Path: csv, Account: "Amex"})
	if err != nil || p.Account != "Amex" {
		t.Errorf("preview %+v, %v; want Amex", p, err)
	}
}

// Same day and amount as an already-imported row, under a new bank id: kept,
// but called out.
func TestPreviewFlagsPossibleDuplicates(t *testing.T) {
	a, _ := seeded(t)
	head := `"Transaction ID","Posting Date","Amount","Description"` + "\n"
	first := writeCSV(t, head+`"X-1","9/5/2026","-37.44","Amazon"`+"\n")
	p, err := a.PreviewImport(ImportRequest{Path: first})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ImportCSV(ImportRequest{Path: first, Fingerprint: p.Fingerprint}); err != nil {
		t.Fatal(err)
	}

	renumbered := writeCSV(t, head+`"Y-77","9/5/2026","-37.44","Amazon"`+"\n")
	p, err = a.PreviewImport(ImportRequest{Path: renumbered})
	if err != nil {
		t.Fatal(err)
	}
	if p.New != 1 || len(p.Suspects) != 1 || p.Suspects[0].ExistingID == 0 {
		t.Errorf("preview new=%d suspects=%+v, want one new row flagged", p.New, p.Suspects)
	}
}
