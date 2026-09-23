package main

import "sort"

// ReportRow is one category's budget-vs-actual line for a single month.
// ActualCents is stated in "natural" terms: spend for expense categories,
// earnings for income categories — both positive in the ordinary case.
// A refund can push an expense row negative; that is real and shown as-is.
type ReportRow struct {
	CategoryID  int     `json:"category_id"`
	Category    string  `json:"category"`
	Kind        string  `json:"kind"`
	BudgetCents int64   `json:"budget_cents"`
	HasBudget   bool    `json:"has_budget"`
	ActualCents int64   `json:"actual_cents"`
	RemainCents int64   `json:"remaining_cents"`
	PercentUsed float64 `json:"percent_used"` // 0 when unbudgeted
	OverBudget  bool    `json:"over_budget"`
	TxnCount    int     `json:"txn_count"`
}

type Report struct {
	Month              string      `json:"month"`
	Expenses           []ReportRow `json:"expenses"`
	Income             []ReportRow `json:"income"`
	TotalBudgetCents   int64       `json:"total_budget_cents"`
	TotalSpentCents    int64       `json:"total_spent_cents"`
	TotalIncomeCents   int64       `json:"total_income_cents"`
	UncategorizedCents int64       `json:"uncategorized_cents"`
	UncategorizedCount int         `json:"uncategorized_count"`
	NetCents           int64       `json:"net_cents"`
}

// BuildReport aggregates one month. Every category that has either a budget
// or activity in the month gets a row; silent categories are omitted so the
// report stays as short as the month actually was.
func BuildReport(db *DB, month string) Report {
	rep := Report{Month: month}

	sums := map[int]int64{} // categoryID -> signed cents
	counts := map[int]int{}
	for _, t := range db.Transactions {
		if MonthOf(t.Date) != month {
			continue
		}
		if t.CategoryID == 0 {
			rep.UncategorizedCents += t.AmountCents
			rep.UncategorizedCount++
			continue
		}
		sums[t.CategoryID] += t.AmountCents
		counts[t.CategoryID]++
	}

	for _, c := range db.Categories {
		budget, hasBudget := db.BudgetFor(c.ID, month)
		signed, hasActivity := sums[c.ID]
		if !hasBudget && !hasActivity {
			continue
		}

		actual := signed
		if c.Kind == KindExpense {
			actual = -signed // spending is stored negative; report it positive
		}

		row := ReportRow{
			CategoryID:  c.ID,
			Category:    c.Name,
			Kind:        c.Kind,
			BudgetCents: budget,
			HasBudget:   hasBudget,
			ActualCents: actual,
			TxnCount:    counts[c.ID],
		}
		if hasBudget {
			row.RemainCents = budget - actual
			row.OverBudget = actual > budget
			if budget > 0 {
				row.PercentUsed = float64(actual) / float64(budget) * 100
			} else if actual > 0 {
				row.PercentUsed = 100
			}
		}

		if c.Kind == KindExpense {
			rep.Expenses = append(rep.Expenses, row)
			rep.TotalSpentCents += actual
			rep.TotalBudgetCents += budget
		} else {
			rep.Income = append(rep.Income, row)
			rep.TotalIncomeCents += actual
		}
	}

	// Biggest spend first — that is what you scan a budget report for.
	sort.SliceStable(rep.Expenses, func(i, j int) bool {
		return rep.Expenses[i].ActualCents > rep.Expenses[j].ActualCents
	})
	sort.SliceStable(rep.Income, func(i, j int) bool {
		return rep.Income[i].ActualCents > rep.Income[j].ActualCents
	})

	rep.NetCents = rep.TotalIncomeCents - rep.TotalSpentCents + rep.UncategorizedCents
	return rep
}

// MonthTotal is one point on the spending trend.
type MonthTotal struct {
	Month       string `json:"month"`
	SpentCents  int64  `json:"spent_cents"`
	IncomeCents int64  `json:"income_cents"`
	BudgetCents int64  `json:"budget_cents"`
}

// Trend builds the per-month totals for a list of months (oldest first).
func Trend(db *DB, months []string) []MonthTotal {
	out := make([]MonthTotal, 0, len(months))
	for _, m := range months {
		mt := MonthTotal{Month: m}
		for _, t := range db.Transactions {
			if MonthOf(t.Date) != m || t.CategoryID == 0 {
				continue
			}
			c := db.CategoryByID(t.CategoryID)
			if c == nil {
				continue
			}
			if c.Kind == KindExpense {
				mt.SpentCents += -t.AmountCents
			} else {
				mt.IncomeCents += t.AmountCents
			}
		}
		for _, b := range db.Budgets {
			if b.Month != m {
				continue
			}
			if c := db.CategoryByID(b.CategoryID); c != nil && c.Kind == KindExpense {
				mt.BudgetCents += b.AmountCents
			}
		}
		out = append(out, mt)
	}
	return out
}
