package main

import (
	"os"
	"path/filepath"
	"testing"
)

// importDB is a single-account book with the categories the fixtures refer to.
func importDB() *DB {
	db := &DB{
		Accounts:      []Account{{ID: 1, Name: "Veridian Checking", Type: "checking"}},
		NextAccountID: 2, NextCategoryID: 1, NextTransactionID: 1,
	}
	for _, c := range []struct{ name, kind string }{
		{"Paycheck", KindIncome}, {"Groceries", KindExpense}, {"Gas", KindExpense},
		{"Eating-Out", KindExpense}, {"Shopping", KindExpense}, {"Utilities", KindExpense},
	} {
		db.Categories = append(db.Categories, Category{ID: db.NextCategoryID, Name: c.name, Kind: c.kind})
		db.NextCategoryID++
	}
	return db
}

func runImport(t *testing.T, db *DB, file string, opts importOptions) (*importResult, error) {
	t.Helper()
	if opts.Cols == nil {
		opts.Cols = map[string]string{}
	}
	return ImportCSV(db, filepath.Join("testdata", file), 1, opts)
}

func mustImport(t *testing.T, db *DB, file string, opts importOptions) *importResult {
	t.Helper()
	res, err := runImport(t, db, file, opts)
	if err != nil {
		t.Fatalf("import %s: %v", file, err)
	}
	return res
}

func txnByDesc(db *DB, desc string) *Transaction {
	for i := range db.Transactions {
		if db.Transactions[i].Description == desc {
			return &db.Transactions[i]
		}
	}
	return nil
}

func TestNormalizeAmount(t *testing.T) {
	cases := []struct {
		in    string
		sep   byte
		cents int64
	}{
		{"-8.01000", '.', -801},   // bank padding to 5dp
		{"893.68000", '.', 89368}, // positive, unsigned
		{"(37.44)", '.', -3744},   // accounting negative
		{"37.44-", '.', -3744},    // trailing sign
		{"$1,299.99", '.', 129999},
		{"$5", '.', 500},
		{"-$45.20", '.', -4520},
		{"+24.99", '.', 2499},
		{"0.00000", '.', 0},
		{"37,44", ',', 3744},      // decimal comma
		{"1.299,99", ',', 129999}, // dot as thousands separator
		{"-37,44", ',', -3744},
	}
	for _, c := range cases {
		norm, err := normalizeAmount(c.in, c.sep)
		if err != nil {
			t.Errorf("normalizeAmount(%q) errored: %v", c.in, err)
			continue
		}
		got, _, err := ParseMoney(norm)
		if err != nil {
			t.Errorf("normalizeAmount(%q) = %q, which ParseMoney rejects: %v", c.in, norm, err)
			continue
		}
		if got != c.cents {
			t.Errorf("%q -> %q -> %d cents, want %d", c.in, norm, got, c.cents)
		}
	}
}

// Padding may be dropped; real precision may not. Rounding here would silently
// change the number.
func TestNormalizeAmountRejectsRealPrecision(t *testing.T) {
	for _, in := range []string{"1.005", "8.0101", "-2.4567"} {
		if _, err := normalizeAmount(in, '.'); err == nil {
			t.Errorf("normalizeAmount(%q) should refuse to round", in)
		}
	}
	if got, _ := normalizeAmount("", '.'); got != "" {
		t.Errorf("an empty cell should stay empty, got %q", got)
	}
}

func TestParseCSVDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-09-22", "2026-09-22"},
		{"9/22/2026", "2026-09-22"},
		{"09/22/2026", "2026-09-22"},
		{"9/22/26", "2026-09-22"},
		{"22-Sep-2026", "2026-09-22"},
		{"Sep 22, 2026", "2026-09-22"},
		{"22.09.2026", "2026-09-22"},
		{"", ""},
	}
	for _, c := range cases {
		got, err := parseCSVDate(c.in, "")
		if err != nil {
			t.Errorf("parseCSVDate(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseCSVDate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Day-first files need to say so; the default reading is month-first.
	if got, _ := parseCSVDate("04/03/2026", ""); got != "2026-04-03" {
		t.Errorf("slashed dates should read month-first, got %q", got)
	}
	if got, _ := parseCSVDate("04/03/2026", "02/01/2006"); got != "2026-03-04" {
		t.Errorf("--date-format ignored, got %q", got)
	}
	if _, err := parseCSVDate("not a date", ""); err == nil {
		t.Error("an unparseable date should error, not be guessed")
	}
}

// The regression this feature is most likely to grow: an unsigned positive row
// must not be flipped by the category-direction inference.
func TestImportKeepsPositiveAmountsPositive(t *testing.T) {
	db := importDB()
	mustImport(t, db, "signed.csv", importOptions{})

	pay := txnByDesc(db, "Salary from Acme Corp")
	if pay == nil {
		t.Fatal("the salary row was not imported")
	}
	if pay.AmountCents != 89368 {
		t.Errorf("salary = %d cents, want +89368", pay.AmountCents)
	}
	if amazon := txnByDesc(db, "Amazon"); amazon == nil || amazon.AmountCents != -3744 {
		t.Errorf("Amazon = %v, want -3744", amazon)
	}
	// 5-decimal padding must survive as exact cents.
	if chase := txnByDesc(db, "Payment to Chase"); chase == nil || chase.AmountCents != -801 {
		t.Errorf("Chase payment = %v, want -801", chase)
	}
}

func TestImportSkipsAreCountedNotSilent(t *testing.T) {
	db := importDB()
	res := mustImport(t, db, "signed.csv", importOptions{})

	if res.Rows != 8 {
		t.Errorf("read %d rows, want 8", res.Rows)
	}
	if res.Imported != 5 {
		t.Errorf("imported %d, want 5", res.Imported)
	}
	if res.Pending != 1 {
		t.Errorf("pending %d, want 1", res.Pending)
	}
	if res.Zero != 1 {
		t.Errorf("zero-amount %d, want 1", res.Zero)
	}
	if res.Junk != 1 { // the trailing "Totals" line
		t.Errorf("junk %d, want 1", res.Junk)
	}
	// Everything read is accounted for somewhere.
	if total := res.Imported + res.Pending + res.Zero + res.Junk + res.Duplicates; total != res.Rows {
		t.Errorf("%d rows accounted for, but %d were read", total, res.Rows)
	}
	if txnByDesc(db, "Card Hold; McDonalds") != nil {
		t.Error("a pending card hold was imported")
	}
}

// A blank primary date falls back to the secondary date column.
func TestImportDateFallback(t *testing.T) {
	db := importDB()
	mustImport(t, db, "signed.csv", importOptions{})
	bg := txnByDesc(db, "Bread Garden")
	if bg == nil {
		t.Fatal("the row with a blank posting date was dropped")
	}
	if bg.Date != "2026-09-13" {
		t.Errorf("date = %q, want the effective date 2026-09-13", bg.Date)
	}
}

func TestImportCategoryResolution(t *testing.T) {
	db := importDB()
	mustImport(t, db, "signed.csv", importOptions{})

	cases := []struct{ desc, want string }{
		{"Amazon", "Shopping"},                  // exact name match
		{"Salary from Acme Corp", "Paycheck"},   // alias: Paychecks/Salary
		{"Kum & Go", "Gas"},                     // alias: Gasoline/Fuel
		{"Bread Garden", "Eating-Out"},          // alias: Restaurants & Dining
		{"Payment to Chase", "(uncategorized)"}, // Credit Card Payments has no equivalent
	}
	for _, c := range cases {
		tx := txnByDesc(db, c.desc)
		if tx == nil {
			t.Errorf("%s not imported", c.desc)
			continue
		}
		if got := db.CategoryName(tx.CategoryID); got != c.want {
			t.Errorf("%s -> %s, want %s", c.desc, got, c.want)
		}
	}
}

func TestImportCategoryMapAndDisable(t *testing.T) {
	db := importDB()
	mustImport(t, db, "signed.csv", importOptions{
		Maps: []string{"Credit Card Payments=Utilities"}})
	tx := txnByDesc(db, "Payment to Chase")
	if got := db.CategoryName(tx.CategoryID); got != "Utilities" {
		t.Errorf("--map ignored: got %s", got)
	}

	// A --map naming a category that does not exist is a typo, caught up front.
	if _, err := runImport(t, importDB(), "signed.csv", importOptions{
		Maps: []string{"Shopping=Nonexistent"}}); err == nil {
		t.Error("--map onto a missing category should error")
	}
	if _, err := runImport(t, importDB(), "signed.csv", importOptions{
		Maps: []string{"no equals sign"}}); err == nil {
		t.Error("a malformed --map should error")
	}

	db = importDB()
	res := mustImport(t, db, "signed.csv", importOptions{NoCategory: true})
	if res.Categorized != 0 {
		t.Errorf("--no-category still categorized %d rows", res.Categorized)
	}
}

// An alias pointing at a category the user does not have leaves the row
// uncategorized; nothing is ever auto-created.
func TestImportNeverCreatesCategories(t *testing.T) {
	db := importDB()
	db.Categories = nil // no categories at all
	before := len(db.Categories)
	res := mustImport(t, db, "signed.csv", importOptions{})
	if len(db.Categories) != before {
		t.Errorf("import created %d categories", len(db.Categories)-before)
	}
	if res.Categorized != 0 {
		t.Errorf("categorized %d rows with no categories to use", res.Categorized)
	}
}

func TestImportDedup(t *testing.T) {
	db := importDB()
	first := mustImport(t, db, "signed.csv", importOptions{})
	second := mustImport(t, db, "signed.csv", importOptions{})

	if second.Imported != 0 {
		t.Errorf("re-import added %d rows, want 0", second.Imported)
	}
	if second.Duplicates != first.Imported {
		t.Errorf("duplicates = %d, want %d", second.Duplicates, first.Imported)
	}
	if len(db.Transactions) != first.Imported {
		t.Errorf("%d transactions after two runs, want %d", len(db.Transactions), first.Imported)
	}
	// Imported rows carry the bank id; nothing else does.
	for _, tx := range db.Transactions {
		if tx.ExternalID == "" {
			t.Errorf("imported txn %d has no external id", tx.ID)
		}
	}
	if hand, _ := AddTransaction(db, "2026-09-01", "Veridian Checking", "Gas", "typed by hand", "10"); hand.ExternalID != "" {
		t.Error("a hand-entered transaction should have no external id")
	}
}

func TestImportPairMode(t *testing.T) {
	db := importDB()
	res := mustImport(t, db, "pair.csv", importOptions{})
	if res.Dialect.Mode != modePair {
		t.Fatalf("mode = %v, want pair", res.Dialect.Mode)
	}
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != -3744 {
		t.Errorf("debit row = %v, want -3744", tx)
	}
	if tx := txnByDesc(db, "Paycheck"); tx == nil || tx.AmountCents != 89368 {
		t.Errorf("credit row = %v, want +89368", tx)
	}
	// A zero filling the unused column must not read as "both sides present".
	if tx := txnByDesc(db, "Hy-Vee"); tx == nil || tx.AmountCents != -5615 {
		t.Errorf("row with a blank credit = %v, want -5615", tx)
	}
	// --invert is meaningless when direction is already explicit.
	if _, err := runImport(t, importDB(), "pair.csv", importOptions{Invert: true}); err == nil {
		t.Error("--invert on a debit/credit file should be rejected, not ignored")
	}
}

func TestImportTypedMode(t *testing.T) {
	db := importDB()
	res := mustImport(t, db, "typed.csv", importOptions{})
	if res.Dialect.Mode != modeTyped {
		t.Fatalf("mode = %v, want typed", res.Dialect.Mode)
	}
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != -3744 {
		t.Errorf("Debit row = %v, want -3744", tx)
	}
	if tx := txnByDesc(db, "Paycheck"); tx == nil || tx.AmountCents != 89368 {
		t.Errorf("Credit row = %v, want +89368", tx)
	}
	// An unrecognised type word is assumed outgoing, and said so out loud.
	fee := txnByDesc(db, "Monthly fee")
	if fee == nil || fee.AmountCents != -400 {
		t.Errorf("unknown type row = %v, want -400", fee)
	}
	if res.AssumedOut != 1 {
		t.Errorf("AssumedOut = %d, want 1 so the summary can report it", res.AssumedOut)
	}
}

func TestImportInvertedCardExport(t *testing.T) {
	// Left to itself it must refuse, and import nothing.
	db := importDB()
	_, err := runImport(t, db, "inverted.csv", importOptions{})
	if err == nil {
		t.Fatal("an inverted card export should not import silently")
	}
	if len(db.Transactions) != 0 {
		t.Errorf("%d transactions written by a refused import", len(db.Transactions))
	}

	// --invert makes purchases negative and the payment positive.
	db = importDB()
	mustImport(t, db, "inverted.csv", importOptions{Invert: true})
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != -3744 {
		t.Errorf("inverted purchase = %v, want -3744", tx)
	}
	if tx := txnByDesc(db, "Payment received"); tx == nil || tx.AmountCents != 25000 {
		t.Errorf("inverted payment = %v, want +25000", tx)
	}

	// --no-invert takes the file at its word.
	db = importDB()
	mustImport(t, db, "inverted.csv", importOptions{NoInvert: true})
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != 3744 {
		t.Errorf("--no-invert purchase = %v, want +3744", tx)
	}

	if _, err := runImport(t, importDB(), "inverted.csv", importOptions{Invert: true, NoInvert: true}); err == nil {
		t.Error("--invert with --no-invert should be rejected")
	}
}

func TestImportSemicolonDecimalComma(t *testing.T) {
	db := importDB()
	mustImport(t, db, "semicolon.csv", importOptions{})
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != -3744 {
		t.Errorf("decimal-comma amount = %v, want -3744", tx)
	}
	// The dot here is a thousands separator: 1.299,99 is 1299.99, not 1.29999.
	if tx := txnByDesc(db, "Gehalt"); tx == nil || tx.AmountCents != 129999 {
		t.Errorf("thousands-separated amount = %v, want +129999", tx)
	}
	if tx := txnByDesc(db, "Amazon"); tx != nil && tx.Date != "2026-09-22" {
		t.Errorf("dotted date = %q, want 2026-09-22", tx.Date)
	}
}

func TestImportPreambleAndBOM(t *testing.T) {
	db := importDB()
	res := mustImport(t, db, "preamble.csv", importOptions{})
	if res.Imported != 2 {
		t.Errorf("imported %d, want 2 (the preamble is not data)", res.Imported)
	}
	if tx := txnByDesc(db, "Amazon"); tx == nil || tx.AmountCents != -3744 {
		t.Errorf("row below the preamble = %v, want -3744", tx)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	db := importDB()
	res := mustImport(t, db, "signed.csv", importOptions{DryRun: true})
	if res.Imported == 0 {
		t.Fatal("dry run reported nothing importable")
	}
	if len(db.Transactions) != 0 {
		t.Errorf("dry run appended %d transactions", len(db.Transactions))
	}
	if db.NextTransactionID != 1 {
		t.Errorf("dry run consumed ids, NextTransactionID = %d", db.NextTransactionID)
	}
	if len(res.Preview) == 0 {
		t.Error("dry run produced no preview rows")
	}
}

// A row budgit cannot read aborts the whole import rather than landing half a
// statement in the file.
func TestImportAbortsWholeFileOnBadRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.csv")
	writeFile(t, path, `Date,Description,Amount
09/22/2026,Fine,-10.00
not-a-date,Broken,-20.00
09/20/2026,AlsoFine,-30.00
`)
	db := importDB()
	if _, err := ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}}); err == nil {
		t.Fatal("an unreadable date should abort the import")
	}
	if len(db.Transactions) != 0 {
		t.Errorf("%d transactions written by an aborted import", len(db.Transactions))
	}
}

// The same id twice in one file is still one transaction.
func TestImportDedupWithinOneFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dupes.csv")
	writeFile(t, path, `Transaction ID,Date,Description,Amount
X-1,09/22/2026,Amazon,-37.44
X-1,09/22/2026,Amazon,-37.44
X-2,09/21/2026,Target,-10.00
`)
	db := importDB()
	res, err := ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Duplicates != 1 {
		t.Errorf("imported %d / duplicates %d, want 2 / 1", res.Imported, res.Duplicates)
	}
}

// Two genuinely identical purchases on the same day are two transactions: this
// is why dedup keys on the bank's id and not on date+amount+description.
func TestImportKeepsGenuineRepeats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repeats.csv")
	writeFile(t, path, `Transaction ID,Date,Description,Amount
R-1,09/07/2026,McDonalds,-12.84
R-2,09/07/2026,McDonalds,-12.84
`)
	db := importDB()
	res, err := ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 {
		t.Errorf("imported %d, want both repeats", res.Imported)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A purchase you typed yourself must not come back as a second row when the
// statement arrives. This is the case external-id dedup cannot see on its own.
func TestImportAdoptsHandEnteredRow(t *testing.T) {
	db := importDB()
	hand, err := AddTransaction(db, "2026-09-05", "Veridian Checking", "Shopping", "Amazon", "37.44")
	if err != nil {
		t.Fatal(err)
	}
	handID := hand.ID

	res := mustImport(t, db, "sample-statement.csv", importOptions{})
	if res.Matched != 1 {
		t.Errorf("matched %d, want 1", res.Matched)
	}

	var count int
	for _, tx := range db.Transactions {
		if tx.Date == "2026-09-05" && tx.AmountCents == -3744 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the same purchase appears %d times, want 1", count)
	}

	// What you typed survives; it just gains the bank's id.
	adopted := db.FindTransaction(handID)
	if adopted == nil {
		t.Fatal("the hand-entered row was removed instead of adopted")
	}
	if adopted.Description != "Amazon" || adopted.CategoryID == 0 {
		t.Errorf("adoption overwrote your own description/category: %+v", adopted)
	}
	if adopted.ExternalID == "" {
		t.Error("adopted row carries no bank id, so the next import will duplicate it")
	}

	// And the match sticks: re-importing recognises it outright.
	second := mustImport(t, db, "sample-statement.csv", importOptions{})
	if second.Imported != 0 || second.Matched != 0 {
		t.Errorf("re-import: imported %d matched %d, want 0 and 0", second.Imported, second.Matched)
	}
	if second.Duplicates != res.Imported+1 {
		t.Errorf("re-import saw %d already present, want %d", second.Duplicates, res.Imported+1)
	}
}

// Matching is one-to-one, so genuine repeats are preserved in both directions.
func TestImportAdoptionIsOneToOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twice.csv")
	writeFile(t, path, `Transaction ID,Date,Description,Amount
M-1,09/07/2026,McDonalds,-12.84
M-2,09/07/2026,McDonalds,-12.84
`)
	// Two typed, two on the statement: two transactions, both claimed.
	db := importDB()
	for i := 0; i < 2; i++ {
		if _, err := AddTransaction(db, "2026-09-07", "Veridian Checking", "Eating-Out", "McDonalds", "12.84"); err != nil {
			t.Fatal(err)
		}
	}
	res, err := ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 2 || res.Imported != 0 {
		t.Errorf("matched %d imported %d, want 2 and 0", res.Matched, res.Imported)
	}
	if len(db.Transactions) != 2 {
		t.Errorf("%d transactions, want 2", len(db.Transactions))
	}

	// One typed, two on the statement: one claimed, one genuinely new.
	db = importDB()
	if _, err := AddTransaction(db, "2026-09-07", "Veridian Checking", "Eating-Out", "McDonalds", "12.84"); err != nil {
		t.Fatal(err)
	}
	res, err = ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 1 || res.Imported != 1 {
		t.Errorf("matched %d imported %d, want 1 and 1", res.Matched, res.Imported)
	}
	if len(db.Transactions) != 2 {
		t.Errorf("%d transactions, want 2", len(db.Transactions))
	}
}

// A row on a different account is a different purchase, however alike it looks.
func TestImportAdoptionIsPerAccount(t *testing.T) {
	db := importDB()
	db.Accounts = append(db.Accounts, Account{ID: 2, Name: "Discover Card", Type: "credit"})
	db.NextAccountID = 3
	if _, err := AddTransaction(db, "2026-09-05", "Discover Card", "Shopping", "Amazon", "37.44"); err != nil {
		t.Fatal(err)
	}
	res := mustImport(t, db, "sample-statement.csv", importOptions{}) // imports into account 1
	if res.Matched != 0 {
		t.Errorf("matched %d across accounts, want 0", res.Matched)
	}
}

// Rows that already carry a bank id are settled records and must never be
// re-claimed by a different statement row.
func TestImportNeverClaimsAnImportedRow(t *testing.T) {
	db := importDB()
	mustImport(t, db, "sample-statement.csv", importOptions{})
	before := len(db.Transactions)

	dir := t.TempDir()
	path := filepath.Join(dir, "other.csv")
	// Same date and amount as a row already imported, but a different bank id.
	writeFile(t, path, `Transaction ID,Date,Description,Amount
OTHER-1,09/05/2026,Amazon again,-37.44
`)
	res, err := ImportCSV(db, path, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 0 {
		t.Errorf("matched %d against an already-imported row, want 0", res.Matched)
	}
	if res.Imported != 1 || len(db.Transactions) != before+1 {
		t.Errorf("imported %d, want it recorded as a separate purchase", res.Imported)
	}
}

func TestImportDryRunDoesNotAdopt(t *testing.T) {
	db := importDB()
	hand, err := AddTransaction(db, "2026-09-05", "Veridian Checking", "Shopping", "Amazon", "37.44")
	if err != nil {
		t.Fatal(err)
	}
	res := mustImport(t, db, "sample-statement.csv", importOptions{DryRun: true})
	if res.Matched != 1 {
		t.Errorf("dry run should still report the match, got %d", res.Matched)
	}
	if hand.ExternalID != "" {
		t.Error("dry run attached a bank id to an existing transaction")
	}
}

// writeOverlap builds two exports sharing some rows, the way two downloads with
// overlapping date ranges do.
func writeOverlap(t *testing.T, dir string, withIDs bool) (string, string) {
	t.Helper()
	head, a, b := "Date,Description,Amount\n", "", ""
	if withIDs {
		head = "Transaction ID," + head
	}
	rows := []struct{ id, line string }{
		{"S-1", "09/01/2026,Interest,0.10"},
		{"S-2", "09/05/2026,Amazon,-37.44"},   // in both
		{"S-3", "09/06/2026,Netflix,-15.49"},  // in both
		{"S-4", "09/11/2026,Target,-63.18"},   // b only
		{"S-5", "09/12/2026,Kum & Go,-34.06"}, // b only
	}
	for i, r := range rows {
		line := r.line + "\n"
		if withIDs {
			line = r.id + "," + line
		}
		if i <= 2 {
			a += line
		}
		if i >= 1 {
			b += line
		}
	}
	pa, pb := filepath.Join(dir, "a.csv"), filepath.Join(dir, "b.csv")
	writeFile(t, pa, head+a)
	writeFile(t, pb, head+b)
	return pa, pb
}

// Two downloads covering overlapping dates must not record the shared rows
// twice. With bank ids present this is a straight id match.
func TestImportOverlappingFilesWithIDs(t *testing.T) {
	dir := t.TempDir()
	pa, pb := writeOverlap(t, dir, true)
	db := importDB()

	first, err := ImportCSV(db, pa, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ImportCSV(db, pb, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 3 {
		t.Errorf("file A imported %d, want 3", first.Imported)
	}
	if second.Imported != 2 || second.Duplicates != 2 {
		t.Errorf("file B imported %d / duplicates %d, want 2 and 2", second.Imported, second.Duplicates)
	}
	if len(db.Transactions) != 5 {
		t.Errorf("%d transactions, want 5 distinct", len(db.Transactions))
	}
	if len(second.Suspects) != 0 {
		t.Errorf("a clean id match should not raise a duplicate warning, got %d", len(second.Suspects))
	}
}

// Plenty of banks export no id column at all. The shared rows still must not
// double up: an imported row with no id stays claimable.
func TestImportOverlappingFilesWithoutIDs(t *testing.T) {
	dir := t.TempDir()
	pa, pb := writeOverlap(t, dir, false)
	db := importDB()

	if _, err := ImportCSV(db, pa, 1, importOptions{Cols: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	second, err := ImportCSV(db, pb, 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Matched != 2 {
		t.Errorf("matched %d of the shared rows, want 2", second.Matched)
	}
	if second.Imported != 2 {
		t.Errorf("imported %d, want the 2 genuinely new rows", second.Imported)
	}
	if len(db.Transactions) != 5 {
		t.Errorf("%d transactions, want 5 distinct", len(db.Transactions))
	}
}

// Some banks renumber their ids between exports, which no id match can catch.
// Budgit keeps both rows — two identical purchases in a day are real — but says
// so rather than letting the duplicate pass unnoticed.
func TestImportRenumberedExportIsFlagged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.csv"), `Transaction ID,Date,Description,Amount
A-001,09/05/2026,Amazon,-37.44
`)
	writeFile(t, filepath.Join(dir, "b.csv"), `Transaction ID,Date,Description,Amount
B-991,09/05/2026,Amazon,-37.44
B-992,09/07/2026,Target,-63.18
`)
	db := importDB()
	if _, err := ImportCSV(db, filepath.Join(dir, "a.csv"), 1, importOptions{Cols: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	res, err := ImportCSV(db, filepath.Join(dir, "b.csv"), 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspects) != 1 {
		t.Fatalf("%d duplicate warnings, want 1", len(res.Suspects))
	}
	s := res.Suspects[0]
	if s.Date != "2026-09-05" || s.Cents != -3744 || s.ExistingID == 0 {
		t.Errorf("warning does not identify the clash: %+v", s)
	}
	// Both are kept: only the user can tell a renumbered export from two real
	// purchases of the same amount on the same day.
	if res.Imported != 2 {
		t.Errorf("imported %d, want both rows kept", res.Imported)
	}
}

// One existing row draws one warning, not one per repeat in the incoming file.
func TestImportSuspectsAreOneToOne(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.csv"), `Transaction ID,Date,Description,Amount
A-1,09/07/2026,McDonalds,-12.84
`)
	writeFile(t, filepath.Join(dir, "b.csv"), `Transaction ID,Date,Description,Amount
B-1,09/07/2026,McDonalds,-12.84
B-2,09/07/2026,McDonalds,-12.84
B-3,09/07/2026,McDonalds,-12.84
`)
	db := importDB()
	if _, err := ImportCSV(db, filepath.Join(dir, "a.csv"), 1, importOptions{Cols: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	res, err := ImportCSV(db, filepath.Join(dir, "b.csv"), 1, importOptions{Cols: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspects) != 1 {
		t.Errorf("%d warnings for one existing row, want 1", len(res.Suspects))
	}
}
