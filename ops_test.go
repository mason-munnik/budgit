package main

import "testing"

// testDB is a two-account, two-category book with no transactions yet.
func testDB() *DB {
	return &DB{
		Accounts: []Account{
			{ID: 1, Name: "Veridian Checking", Type: "checking", OpeningBalanceCents: 100000},
			{ID: 2, Name: "Discover Card", Type: "credit", OpeningBalanceCents: -50000},
		},
		Categories: []Category{
			{ID: 1, Name: "Groceries", Kind: KindExpense},
			{ID: 2, Name: "Paycheck", Kind: KindIncome},
		},
		NextAccountID: 3, NextCategoryID: 3, NextTransactionID: 1,
	}
}

// The sign rule is the subtle part, and it now has two callers (CLI and HTTP),
// so it is pinned here rather than in either one.
func TestAddTransactionSigns(t *testing.T) {
	cases := []struct {
		name     string
		category string
		amount   string
		want     int64
	}{
		{"unsigned into an expense category leaves the account", "Groceries", "84.31", -8431},
		{"unsigned into an income category enters the account", "Paycheck", "1235.87", 123587},
		{"explicit + into an expense category is a refund", "Groceries", "+12.49", 1249},
		{"explicit - into an income category is a clawback", "Paycheck", "-20", -2000},
		{"uncategorized and unsigned is assumed spending", "", "9.82", -982},
		{"uncategorized keeps an explicit sign", "", "+9.82", 982},
		{"currency symbols and separators survive", "Groceries", "$1,299.99", -129999},
	}
	for _, c := range cases {
		db := testDB()
		txn, err := AddTransaction(db, "2026-09-04", "Veridian Checking", c.category, "test", c.amount)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if txn.AmountCents != c.want {
			t.Errorf("%s: amount = %d, want %d", c.name, txn.AmountCents, c.want)
		}
	}
}

func TestAddTransactionDefaults(t *testing.T) {
	db := testDB()
	txn, err := AddTransaction(db, "", "Veridian", "groc", "  Trader Joes  ", "10")
	if err != nil {
		t.Fatal(err)
	}
	if txn.Date != Today() {
		t.Errorf("blank date = %q, want today (%q)", txn.Date, Today())
	}
	// Both references were unique substrings, matched case-insensitively.
	if txn.AccountID != 1 || txn.CategoryID != 1 {
		t.Errorf("resolved to account %d / category %d, want 1 / 1", txn.AccountID, txn.CategoryID)
	}
	if txn.Description != "Trader Joes" {
		t.Errorf("description = %q, want it trimmed", txn.Description)
	}
	if txn.ID != 1 || db.NextTransactionID != 2 {
		t.Errorf("id = %d, next = %d; want 1 and 2", txn.ID, db.NextTransactionID)
	}
}

// A blank account is only unambiguous when exactly one account exists.
func TestAddTransactionAccountRequired(t *testing.T) {
	db := testDB()
	if _, err := AddTransaction(db, "2026-09-04", "", "Groceries", "", "10"); err == nil {
		t.Error("blank account with two accounts should error")
	}
	db.Accounts = db.Accounts[:1]
	txn, err := AddTransaction(db, "2026-09-04", "", "Groceries", "", "10")
	if err != nil {
		t.Fatalf("blank account with one account: %v", err)
	}
	if txn.AccountID != 1 {
		t.Errorf("account = %d, want the only account (1)", txn.AccountID)
	}
}

func TestAddTransactionRejects(t *testing.T) {
	cases := []struct{ name, date, account, category, amount string }{
		{"no amount", "2026-09-04", "Veridian Checking", "Groceries", ""},
		{"unparseable amount", "2026-09-04", "Veridian Checking", "Groceries", "abc"},
		{"malformed date", "2026-09-0", "Veridian Checking", "Groceries", "10"},
		{"unknown account", "2026-09-04", "Nowhere Bank", "Groceries", "10"},
		{"unknown category", "2026-09-04", "Veridian Checking", "Grocerys", "10"},
	}
	for _, c := range cases {
		db := testDB()
		if _, err := AddTransaction(db, c.date, c.account, c.category, "", c.amount); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
		if len(db.Transactions) != 0 {
			t.Errorf("%s: a rejected add must not record anything", c.name)
		}
	}
}

func TestDeleteTransaction(t *testing.T) {
	db := testDB()
	for _, amt := range []string{"10", "20", "30"} {
		if _, err := AddTransaction(db, "2026-09-04", "Veridian Checking", "Groceries", amt, amt); err != nil {
			t.Fatal(err)
		}
	}
	nextBefore := db.NextTransactionID

	gone, err := DeleteTransaction(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if gone.AmountCents != -2000 {
		t.Errorf("returned txn amount = %d, want -2000", gone.AmountCents)
	}
	if len(db.Transactions) != 2 {
		t.Fatalf("%d transactions left, want 2", len(db.Transactions))
	}
	if db.Transactions[0].ID != 1 || db.Transactions[1].ID != 3 {
		t.Errorf("remaining ids = %d, %d; want 1, 3", db.Transactions[0].ID, db.Transactions[1].ID)
	}
	// Reusing a freed id would make two different rows collide for anything
	// that already recorded the old one.
	if db.NextTransactionID != nextBefore {
		t.Errorf("NextTransactionID moved to %d, want it left at %d", db.NextTransactionID, nextBefore)
	}
	if _, err := DeleteTransaction(db, 2); err == nil {
		t.Error("deleting the same id twice should error")
	}
}

func TestSetAccountBalance(t *testing.T) {
	db := testDB()
	if _, err := AddTransaction(db, "2026-09-04", "Veridian Checking", "Groceries", "", "84.31"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTransaction(db, "2026-09-05", "Veridian Checking", "Paycheck", "", "1000"); err != nil {
		t.Fatal(err)
	}

	a, fromTxns, err := SetAccountBalance(db, "Veridian Checking", "4233.44")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.AccountBalance(a.ID); got != 423344 {
		t.Errorf("balance = %d, want 423344", got)
	}
	if fromTxns != 91569 { // 1000.00 in, 84.31 out
		t.Errorf("fromTxns = %d, want 91569", fromTxns)
	}
	// Transactions must survive untouched: only the opening balance moves.
	if len(db.Transactions) != 2 {
		t.Errorf("%d transactions, want 2 left alone", len(db.Transactions))
	}

	// Setting the same balance again is a no-op, not a drift.
	opening := a.OpeningBalanceCents
	if _, _, err := SetAccountBalance(db, "Veridian Checking", "4233.44"); err != nil {
		t.Fatal(err)
	}
	if a.OpeningBalanceCents != opening {
		t.Errorf("opening drifted to %d on a repeat set, want %d", a.OpeningBalanceCents, opening)
	}
	if got := db.AccountBalance(a.ID); got != 423344 {
		t.Errorf("balance after repeat = %d, want 423344", got)
	}
}

// A card you owe on is a negative balance.
func TestSetAccountBalanceNegative(t *testing.T) {
	db := testDB()
	a, _, err := SetAccountBalance(db, "Discover", "-499.50")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.AccountBalance(a.ID); got != -49950 {
		t.Errorf("balance = %d, want -49950", got)
	}
}

func TestSetAccountBalanceRejects(t *testing.T) {
	cases := []struct{ name, account, amount string }{
		{"no account", "", "100"},
		{"no amount", "Veridian Checking", ""},
		{"unknown account", "Nowhere Bank", "100"},
		{"unparseable amount", "Veridian Checking", "abc"},
	}
	for _, c := range cases {
		db := testDB()
		if _, _, err := SetAccountBalance(db, c.account, c.amount); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
		if db.Accounts[0].OpeningBalanceCents != 100000 {
			t.Errorf("%s: a rejected set must not move the opening balance", c.name)
		}
	}
}

func TestAddCategory(t *testing.T) {
	db := testDB()
	c, err := AddCategory(db, "  Coffee  ", "expense")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Coffee" {
		t.Errorf("name = %q, want it trimmed", c.Name)
	}
	if c.ID != 3 || db.NextCategoryID != 4 {
		t.Errorf("id = %d, next = %d; want 3 and 4", c.ID, db.NextCategoryID)
	}
	// A blank kind means the common case: money going out.
	d, err := AddCategory(db, "Parking", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindExpense {
		t.Errorf("blank kind = %q, want %q", d.Kind, KindExpense)
	}
	if e, err := AddCategory(db, "Bonus", "INCOME"); err != nil || e.Kind != KindIncome {
		t.Errorf("kind should be case-insensitive: %v %+v", err, e)
	}
}

func TestAddCategoryRejects(t *testing.T) {
	cases := []struct{ name, cat, kind string }{
		{"blank name", "   ", "expense"},
		{"unknown kind", "Coffee", "spending"},
		{"duplicate", "Groceries", "expense"},
		{"duplicate differing only in case", "GROCERIES", "expense"},
	}
	for _, c := range cases {
		db := testDB()
		before := len(db.Categories)
		if _, err := AddCategory(db, c.cat, c.kind); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
		if len(db.Categories) != before {
			t.Errorf("%s: a rejected add must not record anything", c.name)
		}
	}
}

func TestSetCategoryBudget(t *testing.T) {
	db := testDB()
	c, m, cents, err := SetCategoryBudget(db, "Groceries", "2026-09", "600")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != 1 || m != "2026-09" || cents != 60000 {
		t.Errorf("got category %d, month %q, %d cents; want 1, 2026-09, 60000", c.ID, m, cents)
	}

	// An allowance is a size, not a direction: a typed minus sign is dropped.
	if _, _, cents, err = SetCategoryBudget(db, "Groceries", "2026-09", "-450"); err != nil {
		t.Fatal(err)
	}
	if cents != 45000 {
		t.Errorf("negative amount stored as %d, want 45000", cents)
	}
	// Re-setting the same category and month replaces, never appends.
	if len(db.Budgets) != 1 {
		t.Errorf("%d budget rows, want 1 replaced in place", len(db.Budgets))
	}
	if got, ok := db.BudgetFor(1, "2026-09"); !ok || got != 45000 {
		t.Errorf("BudgetFor = %d (%v), want 45000", got, ok)
	}

	// A different month is a different allowance.
	if _, _, _, err := SetCategoryBudget(db, "Groceries", "2026-10", "700"); err != nil {
		t.Fatal(err)
	}
	if len(db.Budgets) != 2 {
		t.Errorf("%d budget rows, want 2 — months are budgeted separately", len(db.Budgets))
	}
	if got, _ := db.BudgetFor(1, "2026-09"); got != 45000 {
		t.Errorf("September moved to %d, want it left at 45000", got)
	}
}

func TestSetCategoryBudgetRejects(t *testing.T) {
	cases := []struct{ name, cat, month, amount string }{
		{"no category", "", "2026-09", "600"},
		{"no amount", "Groceries", "2026-09", ""},
		{"unknown category", "Grocerys", "2026-09", "600"},
		{"malformed month", "Groceries", "2026-9-01", "600"},
		{"unparseable amount", "Groceries", "2026-09", "abc"},
	}
	for _, c := range cases {
		db := testDB()
		if _, _, _, err := SetCategoryBudget(db, c.cat, c.month, c.amount); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
		if len(db.Budgets) != 0 {
			t.Errorf("%s: a rejected set must not record a budget", c.name)
		}
	}
}

// The sign rule on recategorizing: coerce only when the kind actually changed.
// Forcing it on every move turned a refund into a purchase as soon as it was
// filed under a different expense category.
func TestCategorizeTransactionSigns(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
		amount   string
		want     int64
	}{
		{"refund keeps its sign between two expense categories", "Groceries", "Shopping", "+12.49", 1249},
		{"purchase keeps its sign between two expense categories", "Groceries", "Shopping", "84.31", -8431},
		{"expense to income flips it positive", "Groceries", "Paycheck", "84.31", 8431},
		{"income to expense flips it negative", "Paycheck", "Groceries", "1235.87", -123587},
		{"income to income is left alone", "Paycheck", "Venmo", "20.85", 2085},
	}
	for _, c := range cases {
		db := testDB()
		db.Categories = append(db.Categories,
			Category{ID: 3, Name: "Shopping", Kind: KindExpense},
			Category{ID: 4, Name: "Venmo", Kind: KindIncome})
		db.NextCategoryID = 5

		txn, err := AddTransaction(db, "2026-09-04", "Veridian Checking", c.from, "x", c.amount)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		moved, was, err := CategorizeTransaction(db, txn.ID, c.to)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if was != c.from {
			t.Errorf("%s: previous category = %q, want %q", c.name, was, c.from)
		}
		if moved.AmountCents != c.want {
			t.Errorf("%s: amount = %d, want %d", c.name, moved.AmountCents, c.want)
		}
	}
}

// Uncategorized has no kind, so filing it anywhere still sets the direction.
func TestCategorizeUncategorized(t *testing.T) {
	db := testDB()
	txn, err := AddTransaction(db, "2026-09-04", "Veridian Checking", "", "x", "+50")
	if err != nil {
		t.Fatal(err)
	}
	if txn.AmountCents != 5000 {
		t.Fatalf("setup: amount = %d, want 5000", txn.AmountCents)
	}
	moved, was, err := CategorizeTransaction(db, txn.ID, "Groceries")
	if err != nil {
		t.Fatal(err)
	}
	if was != "(uncategorized)" {
		t.Errorf("previous category = %q, want \"(uncategorized)\"", was)
	}
	if moved.AmountCents != -5000 {
		t.Errorf("amount = %d, want -5000", moved.AmountCents)
	}
}

func TestCategorizeTransactionRejects(t *testing.T) {
	db := testDB()
	txn, err := AddTransaction(db, "2026-09-04", "Veridian Checking", "Groceries", "x", "10")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		id   int
		cat  string
	}{
		{"unknown transaction", 999, "Groceries"},
		{"unknown category", txn.ID, "Grocerys"},
		{"no category", txn.ID, ""},
	} {
		if _, _, err := CategorizeTransaction(db, c.id, c.cat); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
		if txn.CategoryID != 1 || txn.AmountCents != -1000 {
			t.Errorf("%s: a rejected move must leave the transaction alone", c.name)
		}
	}
}
