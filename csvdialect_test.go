package main

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func detect(t *testing.T, name string, opts importOptions) (*dialect, [][]string) {
	t.Helper()
	if opts.Cols == nil {
		opts.Cols = map[string]string{}
	}
	d, rows, _, err := detectDialect(loadFixture(t, name), opts)
	if err != nil {
		t.Fatalf("detect %s: %v", name, err)
	}
	return d, rows
}

func TestSniffDelimiter(t *testing.T) {
	cases := []struct {
		file string
		want rune
	}{
		{"signed.csv", ','},
		{"pair.csv", ','},
		{"preamble.csv", ','}, // commas must win despite the colon-bearing preamble
		{"semicolon.csv", ';'},
	}
	for _, c := range cases {
		if got := sniffDelimiter(stripBOM(loadFixture(t, c.file))); got != c.want {
			t.Errorf("%s: sniffed %q, want %q", c.file, got, c.want)
		}
	}
}

// A BOM on the first column name is invisible and breaks every alias match.
func TestStripBOMAndPreamble(t *testing.T) {
	raw := loadFixture(t, "preamble.csv")
	if raw[0] != 0xEF {
		t.Fatal("fixture lost its BOM; this test is not testing anything")
	}
	d, rows := detect(t, "preamble.csv", importOptions{})
	if d.HeaderLine != 4 {
		t.Errorf("header on line %d, want 4 (three preamble lines above it)", d.HeaderLine)
	}
	if d.Date == colAbsent || d.Amount == colAbsent {
		t.Errorf("columns unresolved: date=%d amount=%d", d.Date, d.Amount)
	}
	if len(rows) != 2 {
		t.Errorf("%d data rows, want 2", len(rows))
	}
}

func TestColumnAliasing(t *testing.T) {
	d, _ := detect(t, "signed.csv", importOptions{})
	// "Posting Date" must win over "Effective Date", which becomes the fallback.
	if got := d.Header[d.Date]; got != "Posting Date" {
		t.Errorf("date column = %q, want Posting Date", got)
	}
	if got := d.Header[d.Date2]; got != "Effective Date" {
		t.Errorf("fallback date = %q, want Effective Date", got)
	}
	// "Transaction Type" must win over a bare "Type" when both exist.
	if got := d.Header[d.Type]; got != "Transaction Type" {
		t.Errorf("type column = %q, want Transaction Type", got)
	}
	for _, c := range []struct {
		name string
		idx  int
	}{{"Amount", d.Amount}, {"Description", d.Desc},
		{"Transaction Category", d.Category}, {"Transaction ID", d.ExtID},
		{"Posting Status", d.Status}} {
		if idx := d.Date; idx == colAbsent {
			t.Fatal("date unresolved")
		}
		if c.idx == colAbsent || d.Header[c.idx] != c.name {
			t.Errorf("%s unresolved (got index %d)", c.name, c.idx)
		}
	}
}

func TestColumnOverrides(t *testing.T) {
	// By name: force the effective date to be the primary.
	d, _ := detect(t, "signed.csv", importOptions{Cols: map[string]string{"date": "Effective Date"}})
	if d.Header[d.Date] != "Effective Date" {
		t.Errorf("override by name failed: got %q", d.Header[d.Date])
	}
	// By index.
	d, _ = detect(t, "signed.csv", importOptions{Cols: map[string]string{"desc": "0"}})
	if d.Desc != 0 {
		t.Errorf("override by index failed: got %d", d.Desc)
	}
	// A name that is not there must say so, and list what is.
	_, _, _, err := detectDialect(loadFixture(t, "signed.csv"), importOptions{
		Cols: map[string]string{"amount": "Nope"}})
	if err == nil {
		t.Fatal("expected an error for an unknown column name")
	}
	if !contains(err.Error(), "Nope") || !contains(err.Error(), "Posting Date") {
		t.Errorf("error should name the bad column and list the real ones, got: %v", err)
	}
}

func TestNoAmountColumnRejected(t *testing.T) {
	_, _, _, err := detectDialect(loadFixture(t, "noamount.csv"), importOptions{Cols: map[string]string{}})
	if err == nil {
		t.Fatal("a file with no amount source must be rejected")
	}
	if !contains(err.Error(), "amount") {
		t.Errorf("error should mention the missing amount column, got: %v", err)
	}
}

func TestAmountModeDetection(t *testing.T) {
	cases := []struct {
		file string
		want amountMode
	}{
		{"signed.csv", modeSigned},
		{"pair.csv", modePair},
		{"typed.csv", modeTyped},
		{"inverted.csv", modeSigned}, // a negative row rules out "unsigned + type"
	}
	for _, c := range cases {
		d, _ := detect(t, c.file, importOptions{})
		if d.Mode != c.want {
			t.Errorf("%s: mode = %v, want %v", c.file, d.Mode, c.want)
		}
	}
}

func TestDecimalCommaFollowsSemicolon(t *testing.T) {
	d, _ := detect(t, "semicolon.csv", importOptions{})
	if d.DecimalSep != ',' {
		t.Errorf("decimal separator = %q, want ','", d.DecimalSep)
	}
	d, _ = detect(t, "semicolon.csv", importOptions{Decimal: "dot"})
	if d.DecimalSep != '.' {
		t.Errorf("--decimal dot ignored, got %q", d.DecimalSep)
	}
}

func TestDirectionWord(t *testing.T) {
	for _, s := range []string{"Debit", "debit", "SALE", "Purchase", "Withdrawal", "dr"} {
		if directionWord(s) != -1 {
			t.Errorf("%q should read as money leaving", s)
		}
	}
	for _, s := range []string{"Credit", "Deposit", "refund", "Return", "CR"} {
		if directionWord(s) != 1 {
			t.Errorf("%q should read as money arriving", s)
		}
	}
	// "Payment" is ambiguous on purpose: a card payment is a credit to the card
	// and a debit to the account it came from.
	for _, s := range []string{"Payment", "ACH", "Service Fee", ""} {
		if directionWord(s) != 0 {
			t.Errorf("%q should carry no direction", s)
		}
	}
}

// The file whose signs contradict its own type column must refuse to guess.
func TestSignConflictDetected(t *testing.T) {
	d, rows := detect(t, "inverted.csv", importOptions{})
	err := d.checkSigns(rows)
	if err == nil {
		t.Fatal("inverted card export should have been flagged")
	}
	var sc *signConflictError
	if !asSignConflict(err, &sc) {
		t.Fatalf("wrong error type: %T", err)
	}
	if sc.debitPositive != 3 {
		t.Errorf("debitPositive = %d, want 3", sc.debitPositive)
	}
	if sc.creditNegative != 1 {
		t.Errorf("creditNegative = %d, want 1", sc.creditNegative)
	}
	for _, want := range []string{"--invert", "--no-invert", "Nothing was imported"} {
		if !contains(err.Error(), want) {
			t.Errorf("message should mention %q, got:\n%s", want, err)
		}
	}
}

// A consistent file must not be flagged.
func TestSignConflictQuietOnConsistentFile(t *testing.T) {
	d, rows := detect(t, "signed.csv", importOptions{})
	if err := d.checkSigns(rows); err != nil {
		t.Errorf("a consistent export was flagged: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func asSignConflict(err error, out **signConflictError) bool {
	sc, ok := err.(*signConflictError)
	if ok {
		*out = sc
	}
	return ok
}
