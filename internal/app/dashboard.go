package app

import (
	"sort"
	"time"

	"github.com/mason-munnik/budgit/internal/store"
)

// TxnView is a transaction with its foreign keys resolved, so the page never
// has to join anything.
type TxnView struct {
	ID          int    `json:"id"`
	Date        string `json:"date"`
	Account     string `json:"account"`
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	AmountCents int64  `json:"amount_cents"`
}

type AccountView struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	BalanceCents int64  `json:"balance_cents"`
}

type RuleView struct {
	ID       int    `json:"id"`
	Match    string `json:"match"`
	Category string `json:"category"`
}

// Dashboard is everything the page draws for one month.
type Dashboard struct {
	Month           string             `json:"month"`
	GeneratedAt     string             `json:"generated_at"`
	Accounts        []AccountView      `json:"accounts"`
	Categories      []store.Category   `json:"categories"`
	Report          store.Report       `json:"report"`
	Transactions    []TxnView          `json:"transactions"`
	Trend           []store.MonthTotal `json:"trend"`
	AvailableMonths []string           `json:"available_months"`
	DataFile        string             `json:"data_file"`
	Rules           []RuleView         `json:"rules"`
}

func buildDashboard(db *store.DB, month, path string) Dashboard {
	d := Dashboard{
		Month:       month,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Report:      store.BuildReport(db, month),
		Trend:       store.Trend(db, store.PrevMonths(month, 6)),
		DataFile:    path,
	}

	d.Accounts = []AccountView{}
	for _, a := range db.Accounts {
		d.Accounts = append(d.Accounts, AccountView{a.ID, a.Name, a.Type, db.AccountBalance(a.ID)})
	}

	// The report only carries categories with a budget or activity; the entry
	// form needs every category that exists.
	d.Categories = append([]store.Category{}, db.Categories...)

	db.SortTransactions()
	d.Transactions = []TxnView{}
	for _, t := range db.Transactions {
		if store.MonthOf(t.Date) != month {
			continue
		}
		kind := ""
		if c := db.CategoryByID(t.CategoryID); c != nil {
			kind = c.Kind
		}
		d.Transactions = append(d.Transactions, TxnView{
			ID: t.ID, Date: t.Date, Account: db.AccountName(t.AccountID),
			Category: db.CategoryName(t.CategoryID), Kind: kind,
			Description: t.Description, AmountCents: t.AmountCents,
		})
	}

	d.AvailableMonths = availableMonths(db, month)

	d.Rules = []RuleView{}
	for _, r := range db.Rules {
		d.Rules = append(d.Rules, RuleView{r.ID, r.Match, db.CategoryName(r.CategoryID)})
	}
	return d
}

// availableMonths lists every month with data, plus the one being viewed,
// newest first — so the picker never omits the current selection.
func availableMonths(db *store.DB, current string) []string {
	seen := map[string]bool{current: true}
	for _, t := range db.Transactions {
		seen[store.MonthOf(t.Date)] = true
	}
	for _, b := range db.Budgets {
		seen[b.Month] = true
	}
	months := make([]string, 0, len(seen))
	for m := range seen {
		months = append(months, m)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	return months
}

// latestMonth defaults the dashboard to the newest month holding data,
// falling back to the calendar month.
func latestMonth(db *store.DB) string {
	best := ""
	for _, t := range db.Transactions {
		if m := store.MonthOf(t.Date); m > best {
			best = m
		}
	}
	for _, b := range db.Budgets {
		if b.Month > best {
			best = b.Month
		}
	}
	if best == "" {
		return store.CurrentMonth()
	}
	return best
}
