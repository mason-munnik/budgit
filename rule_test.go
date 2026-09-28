package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustRule(t *testing.T, db *DB, match, cat string) *Rule {
	t.Helper()
	r, err := AddRule(db, match, cat)
	if err != nil {
		t.Fatalf("AddRule(%q, %q): %v", match, cat, err)
	}
	return r
}

func TestMatchRule(t *testing.T) {
	db := importDB()
	mustRule(t, db, "amazon", "Shopping")
	mustRule(t, db, "Amazon Prime", "Utilities") // added later, still wins: longer
	mustRule(t, db, "trader joe", "Groceries")

	cases := []struct{ desc, want string }{
		{"AMAZON RETA* 5R26W6", "Shopping"},
		{"Amazon  PRIME*2K4", "Utilities"}, // longest pattern, whitespace collapsed
		{"TRADER   JOE'S #552", "Groceries"},
		{"Kum & Go", "(uncategorized)"},
		{"", "(uncategorized)"},
	}
	for _, c := range cases {
		if got := db.CategoryName(db.MatchRule(c.desc)); got != c.want {
			t.Errorf("MatchRule(%q) = %s, want %s", c.desc, got, c.want)
		}
	}
}

func TestMatchRuleTiesAndMissingCategories(t *testing.T) {
	db := importDB()
	// Equal length: the lower id wins, whatever order they sit in.
	db.Rules = []Rule{{ID: 5, Match: "shell", CategoryID: 3}, {ID: 2, Match: "kum &", CategoryID: 4}}
	if got := db.CategoryName(db.MatchRule("kum & go shell")); got != "Eating-Out" {
		t.Errorf("tie went to %s, want the lower id (Eating-Out)", got)
	}
	// A rule whose category has gone is skipped, not matched to "(unknown)".
	db.Rules = []Rule{{ID: 1, Match: "shell gas station", CategoryID: 99}, {ID: 2, Match: "shell", CategoryID: 3}}
	if got := db.CategoryName(db.MatchRule("Shell Gas Station 12")); got != "Gas" {
		t.Errorf("dangling rule matched: got %s, want Gas", got)
	}
}

func TestAddAndDeleteRule(t *testing.T) {
	db := importDB()
	for _, c := range []struct{ match, cat string }{
		{"   ", "Groceries"},    // nothing to match
		{"aldi", "Nonexistent"}, // unknown category
		{"aldi", ""},            // no category
	} {
		if _, err := AddRule(db, c.match, c.cat); err == nil {
			t.Errorf("AddRule(%q, %q) should fail", c.match, c.cat)
		}
	}

	a := mustRule(t, db, "aldi", "Groceries")
	if _, err := AddRule(db, "  ALDI ", "Shopping"); err == nil {
		t.Error("a duplicate pattern should be rejected")
	}
	b := mustRule(t, db, "target", "Shopping")
	if a.ID != 1 || b.ID != 2 {
		t.Errorf("ids = %d, %d, want 1, 2", a.ID, b.ID)
	}

	gone, err := DeleteRule(db, 2)
	if err != nil || gone.Match != "target" {
		t.Fatalf("DeleteRule(2) = %+v, %v", gone, err)
	}
	if _, err := DeleteRule(db, 2); err == nil {
		t.Error("deleting a missing rule should fail")
	}
	if c := mustRule(t, db, "costco", "Groceries"); c.ID != 3 {
		t.Errorf("freed id reused: got %d, want 3", c.ID)
	}
}

func TestApplyRules(t *testing.T) {
	db := importDB()
	appendTransaction(db, "2026-09-01", 1, 0, "AMAZON RETA* 1", -2899, "a")     // purchase
	appendTransaction(db, "2026-09-02", 1, 0, "AMAZON REFUND", 1500, "b")       // refund
	appendTransaction(db, "2026-09-03", 1, 3, "Amazon gift for gas", -500, "c") // already filed
	appendTransaction(db, "2026-09-04", 1, 0, "Kum & Go", -3406, "d")           // no rule

	mustRule(t, db, "amazon", "Shopping")
	if n := len(RuleHits(db)); n != 2 {
		t.Fatalf("RuleHits = %d, want 2", n)
	}
	if db.Transactions[0].CategoryID != 0 {
		t.Fatal("RuleHits changed a transaction")
	}

	hits := ApplyRules(db)
	if len(hits) != 2 {
		t.Fatalf("ApplyRules filed %d, want 2", len(hits))
	}
	want := []struct {
		cat   string
		cents int64
	}{{"Shopping", -2899}, {"Shopping", 1500}, {"Gas", -500}, {"(uncategorized)", -3406}}
	for i, w := range want {
		tx := db.Transactions[i]
		if got := db.CategoryName(tx.CategoryID); got != w.cat {
			t.Errorf("%s -> %s, want %s", tx.Description, got, w.cat)
		}
		// The refund must stay a refund: apply never touches the sign.
		if tx.AmountCents != w.cents {
			t.Errorf("%s amount = %d, want %d", tx.Description, tx.AmountCents, w.cents)
		}
	}
	if len(ApplyRules(db)) != 0 {
		t.Error("a second apply found more to do")
	}
}

func TestAddTransactionUsesRules(t *testing.T) {
	db := testDB()
	mustRule(t, db, "trader joe", "Groceries")
	mustRule(t, db, "acme payroll", "Paycheck")

	cases := []struct {
		name, cat, desc, amount string
		wantCat                 string
		want                    int64
	}{
		{"rule decides the category and the direction", "", "Trader Joes", "20", "Groceries", -2000},
		{"an income rule makes an unsigned amount an inflow", "", "ACME PAYROLL 9/15", "1000", "Paycheck", 100000},
		{"an explicit sign still wins", "", "Trader Joes return", "+5", "Groceries", 500},
		{"a typed category beats the rules", "Paycheck", "Trader Joes", "20", "Paycheck", 2000},
		{"no rule, no category", "", "Kum & Go", "10", "(uncategorized)", -1000},
	}
	for _, c := range cases {
		tx, err := AddTransaction(db, "2026-09-04", "Veridian", c.cat, c.desc, c.amount)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := db.CategoryName(tx.CategoryID); got != c.wantCat || tx.AmountCents != c.want {
			t.Errorf("%s: got %s %d, want %s %d", c.name, got, tx.AmountCents, c.wantCat, c.want)
		}
	}
}

func TestImportRules(t *testing.T) {
	// The rule overrides the bank's Shopping label and places what the label could not.
	withRules := func() *DB {
		db := importDB()
		mustRule(t, db, "amazon", "Utilities")
		mustRule(t, db, "payment to chase", "Utilities")
		return db
	}

	db := withRules()
	res := mustImport(t, db, "signed.csv", importOptions{})
	for _, d := range []string{"Amazon", "Payment to Chase"} {
		if got := db.CategoryName(txnByDesc(db, d).CategoryID); got != "Utilities" {
			t.Errorf("%s -> %s, want Utilities (rule)", d, got)
		}
	}
	if res.ByRule != 2 {
		t.Errorf("ByRule = %d, want 2", res.ByRule)
	}

	db = withRules()
	res = mustImport(t, db, "signed.csv", importOptions{NoRules: true})
	if got := db.CategoryName(txnByDesc(db, "Amazon").CategoryID); got != "Shopping" {
		t.Errorf("--no-rules: Amazon -> %s, want the bank's Shopping", got)
	}
	if res.ByRule != 0 {
		t.Errorf("--no-rules: ByRule = %d", res.ByRule)
	}

	// --no-category drops the bank's column only; your rules still run.
	db = withRules()
	res = mustImport(t, db, "signed.csv", importOptions{NoCategory: true})
	if res.Categorized != 2 || res.ByRule != 2 {
		t.Errorf("--no-category: categorized %d (%d by rule), want 2 (2)", res.Categorized, res.ByRule)
	}

	db = withRules()
	res = mustImport(t, db, "signed.csv", importOptions{DryRun: true})
	if res.ByRule != 2 || len(db.Transactions) != 0 {
		t.Errorf("dry run: ByRule %d, %d transactions written", res.ByRule, len(db.Transactions))
	}
}

// A file that has never had a rule must not grow rule keys just by being saved.
func TestRulesStayOutOfOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	old := `{"accounts":[],"categories":[{"id":1,"name":"Groceries","kind":"expense"}],` +
		`"transactions":[],"budgets":[],"next_account_id":1,"next_category_id":2,"next_transaction_id":1}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "rule") {
		t.Errorf("saved file gained rule keys:\n%s", data)
	}

	// Once there are rules, they and the counter round-trip.
	mustRule(t, db, "aldi", "Groceries")
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	db, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Rules) != 1 || db.NextRuleID != 2 {
		t.Errorf("reloaded %d rules, NextRuleID %d; want 1, 2", len(db.Rules), db.NextRuleID)
	}
}

func TestRuleSafety(t *testing.T) {
	db := importDB()
	for _, short := range []string{"a", "co", " a b "} {
		if _, err := AddRule(db, short, "Shopping"); err == nil {
			t.Errorf("AddRule(%q) should be refused as too short", short)
		}
	}
	appendTransaction(db, "2026-09-01", 1, 0, "AMAZON RETA* 1", -2899, "a")
	appendTransaction(db, "2026-09-02", 1, 5, "Amazon Prime", -1499, "b")
	appendTransaction(db, "2026-09-03", 1, 0, "Kum & Go", -3406, "c")
	if all, unc := RuleReach(db, "amazon"); all != 2 || unc != 1 {
		t.Errorf("RuleReach = %d, %d; want 2, 1", all, unc)
	}
}
