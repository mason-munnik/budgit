package store

import (
	"fmt"
	"strings"

	"github.com/mason-munnik/budgit/internal/money"
)

// The mutations below are the single implementation shared by the CLI and the
// HTTP server. They take the same human-typed strings the flags accept, so
// ParseMoney stays the only money parser and the browser never does arithmetic.
// None of them save: the caller decides when to write the file.

// AddTransaction records one transaction. An empty acctRef is allowed only when
// there is exactly one account; an empty catRef leaves it uncategorized.
func AddTransaction(db *DB, date, acctRef, catRef, desc, amount string) (*Transaction, error) {
	if strings.TrimSpace(amount) == "" {
		return nil, fmt.Errorf("an amount is required")
	}
	if strings.TrimSpace(date) == "" {
		date = Today()
	}
	d, err := ValidateDate(date)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(acctRef) == "" {
		if len(db.Accounts) != 1 {
			return nil, fmt.Errorf("an account is required")
		}
		acctRef = db.Accounts[0].Name // unambiguous when there is exactly one
	}
	a, err := db.FindAccount(acctRef)
	if err != nil {
		return nil, err
	}
	cents, explicit, err := money.ParseMoney(amount)
	if err != nil {
		return nil, err
	}

	var c *Category
	if strings.TrimSpace(catRef) != "" {
		if c, err = db.FindCategory(catRef); err != nil {
			return nil, err
		}
	} else if id := db.MatchRule(desc); id != 0 {
		c = db.CategoryByID(id)
	}

	catID := 0
	if c != nil {
		catID = c.ID
		if !explicit {
			// Unsigned: let the category decide which way the money moved.
			if c.Kind == KindExpense {
				cents = -money.Abs(cents)
			} else {
				cents = money.Abs(cents)
			}
		}
	} else if !explicit {
		// No category to infer from; an unsigned amount is assumed spending.
		cents = -money.Abs(cents)
	}

	return AppendTransaction(db, d, a.ID, catID, desc, cents, ""), nil
}

// AppendTransaction is the one place a Transaction is created and handed an ID.
// Callers pass the amount in final signed cents: AddTransaction after it has
// inferred a direction, the CSV importer straight from the file.
func AppendTransaction(db *DB, date string, acctID, catID int, desc string, cents int64, extID string) *Transaction {
	t := Transaction{
		ID: db.NextTransactionID, Date: date, AccountID: acctID,
		CategoryID: catID, Description: strings.TrimSpace(desc),
		AmountCents: cents, ExternalID: extID,
	}
	db.NextTransactionID++
	db.Transactions = append(db.Transactions, t)
	return &db.Transactions[len(db.Transactions)-1]
}

// DeleteTransaction removes one transaction and returns the record it removed,
// so the caller can say what just disappeared. NextTransactionID is deliberately
// left alone: reusing a freed ID would make two different rows share one id in
// anything that already recorded it.
func DeleteTransaction(db *DB, id int) (Transaction, error) {
	for i := range db.Transactions {
		if db.Transactions[i].ID == id {
			gone := db.Transactions[i]
			db.Transactions = append(db.Transactions[:i], db.Transactions[i+1:]...)
			return gone, nil
		}
	}
	return Transaction{}, fmt.Errorf("no transaction with id %d", id)
}

// SetAccountBalance makes the account's CURRENT balance equal want, by solving
// for the opening balance that gets it there. Existing transactions are never
// touched. It returns the account and how much of the balance comes from
// transactions.
func SetAccountBalance(db *DB, acctRef, amount string) (*Account, int64, error) {
	if strings.TrimSpace(acctRef) == "" {
		return nil, 0, fmt.Errorf("an account is required")
	}
	if strings.TrimSpace(amount) == "" {
		return nil, 0, fmt.Errorf("an amount is required")
	}
	// Always explicit here: there is no category to infer a sign from,
	// so "-499.50" means you owe and "4200" means you hold.
	want, _, err := money.ParseMoney(amount)
	if err != nil {
		return nil, 0, err
	}
	a, err := db.FindAccount(acctRef)
	if err != nil {
		return nil, 0, err
	}
	current := db.AccountBalance(a.ID)
	fromTxns := current - a.OpeningBalanceCents
	a.OpeningBalanceCents = want - fromTxns
	return a, fromTxns, nil
}

// AddCategory records a new spending or earning category. Names are unique
// case-insensitively, so "Gas" and "gas" cannot both exist.
func AddCategory(db *DB, name, kind string) (*Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("a category name is required")
	}
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		k = KindExpense
	}
	if k != KindIncome && k != KindExpense {
		return nil, fmt.Errorf("kind must be %q or %q, got %q", KindIncome, KindExpense, kind)
	}
	for _, c := range db.Categories {
		if strings.EqualFold(c.Name, name) {
			return nil, fmt.Errorf("category %q already exists (id %d)", c.Name, c.ID)
		}
	}
	c := Category{ID: db.NextCategoryID, Name: name, Kind: k}
	db.NextCategoryID++
	db.Categories = append(db.Categories, c)
	return &db.Categories[len(db.Categories)-1], nil
}

// SetCategoryBudget sets one category's allowance for one month, replacing any
// allowance already recorded for that pair. It returns the category, the
// normalized month and the stored cents so the caller can report what it did.
func SetCategoryBudget(db *DB, catRef, month, amount string) (*Category, string, int64, error) {
	if strings.TrimSpace(catRef) == "" {
		return nil, "", 0, fmt.Errorf("a category is required")
	}
	if strings.TrimSpace(amount) == "" {
		return nil, "", 0, fmt.Errorf("an amount is required")
	}
	if strings.TrimSpace(month) == "" {
		month = CurrentMonth()
	}
	m, err := ValidateMonth(month)
	if err != nil {
		return nil, "", 0, err
	}
	cents, _, err := money.ParseMoney(amount)
	if err != nil {
		return nil, "", 0, err
	}
	cents = money.Abs(cents) // a budget is an allowance, always positive
	c, err := db.FindCategory(catRef)
	if err != nil {
		return nil, "", 0, err
	}
	db.SetBudget(c.ID, m, cents)
	return c, m, cents, nil
}

// CategorizeTransaction refiles a transaction, returning it alongside the name
// of the category it used to sit in.
func CategorizeTransaction(db *DB, id int, catRef string) (*Transaction, string, error) {
	if strings.TrimSpace(catRef) == "" {
		return nil, "", fmt.Errorf("a category is required")
	}
	t := db.FindTransaction(id)
	if t == nil {
		return nil, "", fmt.Errorf("no transaction with id %d", id)
	}
	c, err := db.FindCategory(catRef)
	if err != nil {
		return nil, "", err
	}

	was := db.CategoryName(t.CategoryID)
	// Uncategorized counts as spending, like an unsigned AddTransaction, so a
	// refund filed under an expense stays a refund.
	oldKind := KindExpense
	if old := db.CategoryByID(t.CategoryID); old != nil {
		oldKind = old.Kind
	}
	t.CategoryID = c.ID
	// Only coerce when the direction of the money actually changed. Forcing the
	// sign on every move would turn a refund into a purchase the moment it was
	// filed under a different expense category.
	if oldKind != c.Kind {
		if c.Kind == KindExpense {
			t.AmountCents = -money.Abs(t.AmountCents)
		} else {
			t.AmountCents = money.Abs(t.AmountCents)
		}
	}
	return t, was, nil
}

const minRuleLen = 3

// AddRule saves a pattern; patterns are unique after normalization.
func AddRule(db *DB, match, catRef string) (*Rule, error) {
	match = strings.TrimSpace(match)
	if normMatch(match) == "" {
		return nil, fmt.Errorf("a rule needs some text to match")
	}
	if n := len([]rune(strings.ReplaceAll(normMatch(match), " ", ""))); n < minRuleLen {
		return nil, fmt.Errorf("%q is too short to match safely; use at least %d letters of the merchant name", match, minRuleLen)
	}
	if strings.TrimSpace(catRef) == "" {
		return nil, fmt.Errorf("a category is required")
	}
	c, err := db.FindCategory(catRef)
	if err != nil {
		return nil, err
	}
	for _, r := range db.Rules {
		if normMatch(r.Match) == normMatch(match) {
			return nil, fmt.Errorf("rule %d already matches %q (delete it first: budgit rule delete %d)", r.ID, r.Match, r.ID)
		}
	}
	if db.NextRuleID < 1 {
		db.NextRuleID = 1
	}
	r := Rule{ID: db.NextRuleID, Match: match, CategoryID: c.ID}
	db.NextRuleID++
	db.Rules = append(db.Rules, r)
	return &db.Rules[len(db.Rules)-1], nil
}

// DeleteRule removes one rule. IDs are never reused.
func DeleteRule(db *DB, id int) (Rule, error) {
	for i := range db.Rules {
		if db.Rules[i].ID == id {
			gone := db.Rules[i]
			db.Rules = append(db.Rules[:i], db.Rules[i+1:]...)
			return gone, nil
		}
	}
	return Rule{}, fmt.Errorf("no rule with id %d", id)
}

// RuleHit is one uncategorized transaction a rule would file.
type RuleHit struct {
	TxnID      int
	CategoryID int
}

// RuleHits lists what ApplyRules would file, changing nothing.
func RuleHits(db *DB) []RuleHit {
	var hits []RuleHit
	for _, t := range db.Transactions {
		if t.CategoryID != 0 {
			continue
		}
		if id := db.MatchRule(t.Description); id != 0 {
			hits = append(hits, RuleHit{t.ID, id})
		}
	}
	return hits
}

// ApplyRules files uncategorized matches. It sets only the category:
// CategorizeTransaction would force the sign and turn a refund into a purchase.
func ApplyRules(db *DB) []RuleHit {
	hits := RuleHits(db)
	for _, h := range hits {
		db.FindTransaction(h.TxnID).CategoryID = h.CategoryID
	}
	return hits
}

// AddAccount records an account; balance is its signed opening balance.
func AddAccount(db *DB, name, typ, balance string) (*Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("an account name is required")
	}
	if err := accountNameFree(db, name, 0); err != nil {
		return nil, err
	}
	typ = strings.TrimSpace(typ)
	if typ == "" {
		typ = "checking"
	}
	var opening int64
	if strings.TrimSpace(balance) != "" {
		var err error
		if opening, _, err = money.ParseMoney(balance); err != nil {
			return nil, err
		}
	}
	a := Account{ID: db.NextAccountID, Name: name, Type: typ, OpeningBalanceCents: opening}
	db.NextAccountID++
	db.Accounts = append(db.Accounts, a)
	return &db.Accounts[len(db.Accounts)-1], nil
}

// accountNameFree rejects a name used by any account other than self.
func accountNameFree(db *DB, name string, self int) error {
	for _, a := range db.Accounts {
		if a.ID != self && strings.EqualFold(a.Name, name) {
			return fmt.Errorf("account %q already exists (id %d)", a.Name, a.ID)
		}
	}
	return nil
}

func RenameAccount(db *DB, ref, name string) (*Account, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("a new name is required")
	}
	a, err := db.FindAccount(ref)
	if err != nil {
		return nil, "", err
	}
	if err := accountNameFree(db, name, a.ID); err != nil {
		return nil, "", err
	}
	was := a.Name
	a.Name = name
	return a, was, nil
}

// DeleteAccount refuses an account with transactions rather than orphan them.
func DeleteAccount(db *DB, ref string) (Account, error) {
	a, err := db.FindAccount(ref)
	if err != nil {
		return Account{}, err
	}
	n := 0
	for _, t := range db.Transactions {
		if t.AccountID == a.ID {
			n++
		}
	}
	if n > 0 {
		return Account{}, fmt.Errorf("%s still has %d %s; move or delete %s first",
			a.Name, n, plural(n, "transaction", "transactions"), plural(n, "it", "them"))
	}
	for i := range db.Accounts {
		if db.Accounts[i].ID == a.ID {
			gone := db.Accounts[i]
			db.Accounts = append(db.Accounts[:i], db.Accounts[i+1:]...)
			return gone, nil
		}
	}
	return Account{}, fmt.Errorf("no account with id %d", a.ID)
}

func RenameCategory(db *DB, ref, name string) (*Category, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("a new name is required")
	}
	c, err := db.FindCategory(ref)
	if err != nil {
		return nil, "", err
	}
	for _, o := range db.Categories {
		if o.ID != c.ID && strings.EqualFold(o.Name, name) {
			return nil, "", fmt.Errorf("category %q already exists (id %d)", o.Name, o.ID)
		}
	}
	was := c.Name
	c.Name = name
	return c, was, nil
}

type CategoryRemoval struct {
	Txns, Budgets, Rules int
}

// DeleteCategory uncategorizes its transactions and drops its budgets and rules.
func DeleteCategory(db *DB, ref string) (Category, CategoryRemoval, error) {
	var n CategoryRemoval
	c, err := db.FindCategory(ref)
	if err != nil {
		return Category{}, n, err
	}
	gone := *c
	for i := range db.Transactions {
		if db.Transactions[i].CategoryID == gone.ID {
			db.Transactions[i].CategoryID = 0
			n.Txns++
		}
	}
	budgets := db.Budgets[:0]
	for _, b := range db.Budgets {
		if b.CategoryID == gone.ID {
			n.Budgets++
			continue
		}
		budgets = append(budgets, b)
	}
	db.Budgets = budgets
	rules := db.Rules[:0]
	for _, r := range db.Rules {
		if r.CategoryID == gone.ID {
			n.Rules++
			continue
		}
		rules = append(rules, r)
	}
	db.Rules = rules
	for i := range db.Categories {
		if db.Categories[i].ID == gone.ID {
			db.Categories = append(db.Categories[:i], db.Categories[i+1:]...)
			break
		}
	}
	return gone, n, nil
}

// UncategorizeTransaction leaves the amount's sign as it is.
func UncategorizeTransaction(db *DB, id int) (*Transaction, string, error) {
	t := db.FindTransaction(id)
	if t == nil {
		return nil, "", fmt.Errorf("no transaction with id %d", id)
	}
	was := db.CategoryName(t.CategoryID)
	t.CategoryID = 0
	return t, was, nil
}

// TxnEdit is a partial update; nil fields are left alone.
type TxnEdit struct {
	Date        *string `json:"date,omitempty"`
	Account     *string `json:"account,omitempty"`
	Description *string `json:"description,omitempty"`
	Amount      *string `json:"amount,omitempty"`
}

// EditTransaction validates every field before changing any. An unsigned
// amount keeps the transaction's current direction.
func EditTransaction(db *DB, id int, e TxnEdit) (*Transaction, error) {
	t := db.FindTransaction(id)
	if t == nil {
		return nil, fmt.Errorf("no transaction with id %d", id)
	}
	date, acctID, desc, cents := t.Date, t.AccountID, t.Description, t.AmountCents
	if e.Date != nil {
		d, err := ValidateDate(strings.TrimSpace(*e.Date))
		if err != nil {
			return nil, err
		}
		date = d
	}
	if e.Account != nil {
		a, err := db.FindAccount(*e.Account)
		if err != nil {
			return nil, err
		}
		acctID = a.ID
	}
	if e.Description != nil {
		desc = strings.TrimSpace(*e.Description)
	}
	if e.Amount != nil {
		c, explicit, err := money.ParseMoney(*e.Amount)
		if err != nil {
			return nil, err
		}
		if !explicit {
			out := t.AmountCents < 0
			if t.AmountCents == 0 {
				cat := db.CategoryByID(t.CategoryID)
				out = cat == nil || cat.Kind == KindExpense
			}
			if c = money.Abs(c); out {
				c = -c
			}
		}
		cents = c
	}
	t.Date, t.AccountID, t.Description, t.AmountCents = date, acctID, desc, cents
	return t, nil
}

// RuleReach counts the transactions a pattern matches, and how many are uncategorized.
func RuleReach(db *DB, match string) (all, uncategorized int) {
	m := normMatch(match)
	if m == "" {
		return 0, 0
	}
	for _, t := range db.Transactions {
		if strings.Contains(normMatch(t.Description), m) {
			all++
			if t.CategoryID == 0 {
				uncategorized++
			}
		}
	}
	return all, uncategorized
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
