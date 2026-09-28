package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	// OpeningBalanceCents is the account's balance before any recorded
	// transaction. It is deliberately NOT a transaction: it must count toward
	// the balance without ever appearing as income or spending in a report.
	OpeningBalanceCents int64 `json:"opening_balance_cents"`
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
	// ExternalID is the bank's own id for an imported row, and is what makes a
	// re-import a no-op. Empty for anything entered by hand — omitempty keeps it
	// out of files that have never been imported into.
	ExternalID string `json:"external_id,omitempty"`
}

// Rule files transactions whose description contains Match under a category.
type Rule struct {
	ID         int    `json:"id"`
	Match      string `json:"match"`
	CategoryID int    `json:"category_id"`
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
	// omitempty keeps both rule fields out of files that have never had a rule.
	Rules []Rule `json:"rules,omitempty"`

	NextAccountID     int `json:"next_account_id"`
	NextCategoryID    int `json:"next_category_id"`
	NextTransactionID int `json:"next_transaction_id"`
	NextRuleID        int `json:"next_rule_id,omitempty"` // 0 means 1; see AddRule

	path string
	// loaded is the hash of the file Load read; nil if there was none.
	loaded []byte
}

// ErrChangedOnDisk means another process wrote the file after Load.
var ErrChangedOnDisk = errors.New("the data file changed since it was read (is another budgit running?) — nothing saved, try again")

func fingerprint(data []byte, exists bool) []byte {
	if !exists {
		return nil
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

func onDisk(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the user's own --file/BUDGIT_FILE, never web input
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return fingerprint(data, true), nil
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
	data, err := os.ReadFile(path) // #nosec G304 -- path is the user's own --file/BUDGIT_FILE, never web input
	if os.IsNotExist(err) {
		return db, nil // first run
	}
	if err != nil {
		return nil, err
	}
	db.loaded = fingerprint(data, true)
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
	for _, r := range db.Rules {
		if r.ID >= db.NextRuleID {
			db.NextRuleID = r.ID + 1
		}
	}
	return db, nil
}

// Save writes atomically (temp file, then rename), and returns
// ErrChangedOnDisk rather than overwrite another process's write.
func (db *DB) Save() error {
	if err := os.MkdirAll(filepath.Dir(db.path), 0o700); err != nil {
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
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write error is the one worth returning
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	// Checked last, to keep the race window small.
	now, err := onDisk(db.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(now, db.loaded) {
		return ErrChangedOnDisk
	}
	if err := os.Rename(tmpName, db.path); err != nil {
		return err
	}
	db.loaded = fingerprint(data, true)
	return nil
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

// AccountBalance is the opening balance plus every transaction on the account.
func (db *DB) AccountBalance(id int) int64 {
	var bal int64
	for _, a := range db.Accounts {
		if a.ID == id {
			bal = a.OpeningBalanceCents
			break
		}
	}
	for _, t := range db.Transactions {
		if t.AccountID == id {
			bal += t.AmountCents
		}
	}
	return bal
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

// ExternalIDs is the set of bank ids already imported into one account.
// Per account, because two banks can both number their rows from 1.
func (db *DB) ExternalIDs(acctID int) map[string]bool {
	seen := make(map[string]bool, len(db.Transactions))
	for _, t := range db.Transactions {
		if t.ExternalID != "" && t.AccountID == acctID {
			seen[t.ExternalID] = true
		}
	}
	return seen
}

func (db *DB) FindTransaction(id int) *Transaction {
	for i := range db.Transactions {
		if db.Transactions[i].ID == id {
			return &db.Transactions[i]
		}
	}
	return nil
}

// BudgetFor returns the allowance set for month, else the latest earlier one,
// and the month it was set. A zero allowance stops a budget, so it reports !ok.
func (db *DB) BudgetFor(categoryID int, month string) (cents int64, from string, ok bool) {
	for _, b := range db.Budgets {
		if b.CategoryID != categoryID || b.Month > month {
			continue
		}
		if b.Month > from {
			cents, from = b.AmountCents, b.Month
		}
	}
	if from == "" || cents == 0 {
		return 0, "", false
	}
	return cents, from, true
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

// normMatch lowercases and collapses whitespace; banks pad names unpredictably.
func normMatch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// MatchRule returns the category for desc, or 0. The longest pattern wins,
// lowest ID breaking ties.
func (db *DB) MatchRule(desc string) int {
	d := normMatch(desc)
	if d == "" {
		return 0
	}
	best, bestLen, bestID := 0, 0, 0
	for _, r := range db.Rules {
		m := normMatch(r.Match)
		if m == "" || !strings.Contains(d, m) || db.CategoryByID(r.CategoryID) == nil {
			continue
		}
		if len(m) > bestLen || (len(m) == bestLen && r.ID < bestID) {
			best, bestLen, bestID = r.CategoryID, len(m), r.ID
		}
	}
	return best
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
