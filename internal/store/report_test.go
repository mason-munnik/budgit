package store

import "testing"

func TestBudgetReportMath(t *testing.T) {
	db := &DB{
		Accounts: []Account{{ID: 1, Name: "A", Type: "checking"}},
		Categories: []Category{
			{ID: 1, Name: "Groceries", Kind: KindExpense},
			{ID: 2, Name: "Salary", Kind: KindIncome},
		},
		Budgets: []Budget{{CategoryID: 1, Month: "2026-09", AmountCents: 60000}},
		Transactions: []Transaction{
			{ID: 1, Date: "2026-09-04", AccountID: 1, CategoryID: 1, AmountCents: -8431},
			{ID: 2, Date: "2026-09-09", AccountID: 1, CategoryID: 1, AmountCents: -13218},
			{ID: 3, Date: "2026-09-20", AccountID: 1, CategoryID: 1, AmountCents: 1249}, // refund
			{ID: 4, Date: "2026-09-01", AccountID: 1, CategoryID: 2, AmountCents: 420000},
			{ID: 5, Date: "2026-08-01", AccountID: 1, CategoryID: 1, AmountCents: -9999}, // other month
			{ID: 6, Date: "2026-09-21", AccountID: 1, CategoryID: 0, AmountCents: -3800}, // uncategorized
		},
	}
	rep := BuildReport(db, "2026-09")

	if len(rep.Expenses) != 1 {
		t.Fatalf("want 1 expense row, got %d", len(rep.Expenses))
	}
	// 84.31 + 132.18 - 12.49 refund = 204.00
	if got := rep.Expenses[0].ActualCents; got != 20400 {
		t.Errorf("net spend = %d, want 20400", got)
	}
	if got := rep.Expenses[0].RemainCents; got != 39600 {
		t.Errorf("remaining = %d, want 39600", got)
	}
	if rep.Expenses[0].OverBudget {
		t.Error("should not be over budget")
	}
	if rep.TotalIncomeCents != 420000 {
		t.Errorf("income = %d, want 420000", rep.TotalIncomeCents)
	}
	if rep.UncategorizedCount != 1 || rep.UncategorizedCents != -3800 {
		t.Errorf("uncategorized = %d/%d, want 1/-3800", rep.UncategorizedCount, rep.UncategorizedCents)
	}
	// 420000 income - 20400 spend + (-3800) uncategorized
	if rep.NetCents != 395800 {
		t.Errorf("net = %d, want 395800", rep.NetCents)
	}
}

func TestPrevMonthsCrossesYearBoundary(t *testing.T) {
	got := PrevMonths("2026-02", 4)
	want := []string{"2025-11", "2025-12", "2026-01", "2026-02"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PrevMonths[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// An opening balance must move the account balance without ever showing up
// as income or spending in the report.
func TestOpeningBalance(t *testing.T) {
	db := &DB{
		Accounts: []Account{
			{ID: 1, Name: "Checking", Type: "checking", OpeningBalanceCents: 420000},
			{ID: 2, Name: "Card", Type: "credit", OpeningBalanceCents: -49950},
			{ID: 3, Name: "Fresh", Type: "cash"},
		},
		Categories: []Category{{ID: 1, Name: "Groceries", Kind: KindExpense}},
		Transactions: []Transaction{
			{ID: 1, Date: "2026-09-04", AccountID: 1, CategoryID: 1, AmountCents: -8431},
		},
	}

	if got := db.AccountBalance(1); got != 411569 { // 4200.00 - 84.31
		t.Errorf("balance = %d, want 411569", got)
	}
	if got := db.AccountBalance(2); got != -49950 { // no transactions
		t.Errorf("credit balance = %d, want -49950", got)
	}
	if got := db.AccountBalance(3); got != 0 {
		t.Errorf("fresh account balance = %d, want 0", got)
	}

	rep := BuildReport(db, "2026-09")
	if rep.TotalIncomeCents != 0 {
		t.Errorf("opening balance leaked into income: %d", rep.TotalIncomeCents)
	}
	if rep.TotalSpentCents != 8431 {
		t.Errorf("spend = %d, want 8431", rep.TotalSpentCents)
	}
}
