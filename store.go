package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	KindIncome  = "income"
	KindExpense = "expense"
)

type Account struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // checking, savings, credit, cash, ...
}

type Category struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // income | expense
}

// Transaction amounts are signed cents: negative leaves the account
// (spending), positive enters it (income, refunds).
type Transaction struct {
	ID          int    `json:"id"`
	Date        string `json:"date"` // YYYY-MM-DD
	AccountID   int    `json:"account_id"`
	CategoryID  int    `json:"category_id"` // 0 == uncategorized
	Description string `json:"description"`
	AmountCents int64  `json:"amount_cents"`
}

// Budget is a positive monthly allowance for one category.
type Budget struct {
	CategoryID  int    `json:"category_id"`
	Month       string `json:"month"` // YYYY-MM
	AmountCents int64  `json:"amount_cents"`
}

type DB struct {
	Accounts     []Account     `json:"accounts"`
	Categories   []Category    `json:"categories"`
	Transactions []Transaction `json:"transactions"`
	Budgets      []Budget      `json:"budgets"`

	NextAccountID     int `json:"next_account_id"`
	NextCategoryID    int `json:"next_category_id"`
	NextTransactionID int `json:"next_transaction_id"`

	path string
}

// DefaultPath is ~/.budgit/budgit.json unless BUDGIT_FILE overrides it.
func DefaultPath() string {
	if p := os.Getenv("BUDGIT_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "budgit.json"
	}
	return filepath.Join(home, ".budgit", "budgit.json")
}

func Load(path string) (*DB, error) {
	db := &DB{path: path, NextAccountID: 1, NextCategoryID: 1, NextTransactionID: 1}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return db, nil // first run
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return db, nil
	}
	if err := json.Unmarshal(data, db); err != nil {
		return nil, fmt.Errorf("%s is not valid budgit data: %w", path, err)
	}
	db.path = path
	// Repair counters so a hand-edited file can't hand out duplicate IDs.
	for _, a := range db.Accounts {
		if a.ID >= db.NextAccountID {
			db.NextAccountID = a.ID + 1
		}
	}
	for _, c := range db.Categories {
		if c.ID >= db.NextCategoryID {
			db.NextCategoryID = c.ID + 1
		}
	}
	for _, t := range db.Transactions {
		if t.ID >= db.NextTransactionID {
			db.NextTransactionID = t.ID + 1
		}
	}
	return db, nil
}

// Save writes atomically: temp file in the same dir, then rename.
func (db *DB) Save() error {
	if err := os.MkdirAll(filepath.Dir(db.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(db.path), ".budgit-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, db.path)
}

// ---- lookup ----

// FindAccount resolves a numeric ID or a case-insensitive name.
func (db *DB) FindAccount(ref string) (*Account, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("no account given")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		for i := range db.Accounts {
			if db.Accounts[i].ID == id {
				return &db.Accounts[i], nil
			}
		}
	}
	var hits []*Account
	for i := range db.Accounts {
		if strings.EqualFold(db.Accounts[i].Name, ref) {
			return &db.Accounts[i], nil
		}
		if strings.Contains(strings.ToLower(db.Accounts[i].Name), strings.ToLower(ref)) {
			hits = append(hits, &db.Accounts[i])
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		var names []string
		for _, h := range hits {
			names = append(names, h.Name)
		}
		return nil, fmt.Errorf("account %q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no account matching %q (try: budgit account list)", ref)
}

func (db *DB) FindCategory(ref string) (*Category, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("no category given")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		for i := range db.Categories {
			if db.Categories[i].ID == id {
				return &db.Categories[i], nil
			}
		}
	}
	var hits []*Category
	for i := range db.Categories {
		if strings.EqualFold(db.Categories[i].Name, ref) {
			return &db.Categories[i], nil
		}
		if strings.Contains(strings.ToLower(db.Categories[i].Name), strings.ToLower(ref)) {
			hits = append(hits, &db.Categories[i])
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		var names []string
		for _, h := range hits {
			names = append(names, h.Name)
		}
		return nil, fmt.Errorf("category %q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no category matching %q (try: budgit category list)", ref)
}

func (db *DB) AccountName(id int) string {
	for _, a := range db.Accounts {
		if a.ID == id {
			return a.Name
		}
	}
	return "(unknown)"
}

func (db *DB) CategoryName(id int) string {
	if id == 0 {
		return "(uncategorized)"
	}
	for _, c := range db.Categories {
		if c.ID == id {
			return c.Name
		}
	}
	return "(unknown)"
}

func (db *DB) CategoryByID(id int) *Category {
	for i := range db.Categories {
		if db.Categories[i].ID == id {
			return &db.Categories[i]
		}
	}
	return nil
}

func (db *DB) FindTransaction(id int) *Transaction {
	for i := range db.Transactions {
		if db.Transactions[i].ID == id {
			return &db.Transactions[i]
		}
	}
	return nil
}

func (db *DB) BudgetFor(categoryID int, month string) (int64, bool) {
	for _, b := range db.Budgets {
		if b.CategoryID == categoryID && b.Month == month {
			return b.AmountCents, true
		}
	}
	return 0, false
}

func (db *DB) SetBudget(categoryID int, month string, cents int64) {
	for i := range db.Budgets {
		if db.Budgets[i].CategoryID == categoryID && db.Budgets[i].Month == month {
			db.Budgets[i].AmountCents = cents
			return
		}
	}
	db.Budgets = append(db.Budgets, Budget{CategoryID: categoryID, Month: month, AmountCents: cents})
}

// SortTransactions orders newest first, with ID as the tiebreaker.
func (db *DB) SortTransactions() {
	sort.SliceStable(db.Transactions, func(i, j int) bool {
		if db.Transactions[i].Date != db.Transactions[j].Date {
			return db.Transactions[i].Date > db.Transactions[j].Date
		}
		return db.Transactions[i].ID > db.Transactions[j].ID
	})
}

// ---- dates ----

func ValidateDate(s string) (string, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", fmt.Errorf("date %q must be YYYY-MM-DD", s)
	}
	return t.Format("2006-01-02"), nil
}

func ValidateMonth(s string) (string, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return "", fmt.Errorf("month %q must be YYYY-MM", s)
	}
	return t.Format("2006-01"), nil
}

func CurrentMonth() string { return time.Now().Format("2006-01") }
func Today() string        { return time.Now().Format("2006-01-02") }

// MonthOf extracts YYYY-MM from a YYYY-MM-DD date.
func MonthOf(date string) string {
	if len(date) >= 7 {
		return date[:7]
	}
	return date
}

// PrevMonths returns the n months ending at (and including) month, oldest first.
func PrevMonths(month string, n int) []string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		t = time.Now()
	}
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, t.AddDate(0, -i, 0).Format("2006-01"))
	}
	return out
}
