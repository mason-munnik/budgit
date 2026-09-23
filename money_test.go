package main

import "testing"

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in       string
		cents    int64
		explicit bool
	}{
		{"0", 0, false},
		{"5", 500, false},
		{"84.31", 8431, false},
		{"5.1", 510, false}, // right-padded: 10 cents, not 1
		{"5.01", 501, false},
		{"$1,299.99", 129999, false},
		{"-45.20", -4520, true},
		{"+24.99", 2499, true},
		{"-$1,800", -180000, true},
		{".50", 50, false},
		{"1234567.89", 123456789, false},
	}
	for _, c := range cases {
		got, explicit, err := ParseMoney(c.in)
		if err != nil {
			t.Errorf("ParseMoney(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.cents {
			t.Errorf("ParseMoney(%q) = %d cents, want %d", c.in, got, c.cents)
		}
		if explicit != c.explicit {
			t.Errorf("ParseMoney(%q) explicitSign = %v, want %v", c.in, explicit, c.explicit)
		}
	}
}

func TestParseMoneyRejects(t *testing.T) {
	for _, in := range []string{"", "abc", "1.234", "$", "-", "1.2.3"} {
		if _, _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) should have errored", in)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{8431, "$84.31"},
		{-180000, "-$1,800.00"},
		{123456789, "$1,234,567.89"},
		{100, "$1.00"},
		{-5, "-$0.05"},
	}
	for _, c := range cases {
		if got := FormatMoney(c.cents); got != c.want {
			t.Errorf("FormatMoney(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

// Round-tripping must be exact — this is the whole reason for integer cents.
func TestMoneyRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 1, 99, 100, 8431, -4520, 123456789, -1} {
		parsed, _, err := ParseMoney(FormatMoney(cents))
		if err != nil {
			t.Fatalf("round trip of %d failed: %v", cents, err)
		}
		if parsed != cents {
			t.Errorf("round trip: %d -> %q -> %d", cents, FormatMoney(cents), parsed)
		}
	}
}

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

func TestCheckLoopback(t *testing.T) {
	for _, ok := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:9000"} {
		if err := checkLoopback(ok); err != nil {
			t.Errorf("checkLoopback(%q) rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:8080", "192.168.1.5:8080", ":8080"} {
		if err := checkLoopback(bad); err == nil {
			t.Errorf("checkLoopback(%q) should have been rejected", bad)
		}
	}
}
