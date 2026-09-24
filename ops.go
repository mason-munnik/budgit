package main

import (
	"fmt"
	"strings"
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
	cents, explicit, err := ParseMoney(amount)
	if err != nil {
		return nil, err
	}

	catID := 0
	if strings.TrimSpace(catRef) != "" {
		c, err := db.FindCategory(catRef)
		if err != nil {
			return nil, err
		}
		catID = c.ID
		if !explicit {
			// Unsigned: let the category decide which way the money moved.
			if c.Kind == KindExpense {
				cents = -abs(cents)
			} else {
				cents = abs(cents)
			}
		}
	} else if !explicit {
		// No category to infer from; an unsigned amount is assumed spending.
		cents = -abs(cents)
	}

	t := Transaction{
		ID: db.NextTransactionID, Date: d, AccountID: a.ID,
		CategoryID: catID, Description: strings.TrimSpace(desc), AmountCents: cents,
	}
	db.NextTransactionID++
	db.Transactions = append(db.Transactions, t)
	return &db.Transactions[len(db.Transactions)-1], nil
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
	want, _, err := ParseMoney(amount)
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
	cents, _, err := ParseMoney(amount)
	if err != nil {
		return nil, "", 0, err
	}
	cents = abs(cents) // a budget is an allowance, always positive
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
	oldKind := "" // uncategorized has no kind, and never matches a real one
	if old := db.CategoryByID(t.CategoryID); old != nil {
		oldKind = old.Kind
	}
	t.CategoryID = c.ID
	// Only coerce when the direction of the money actually changed. Forcing the
	// sign on every move would turn a refund into a purchase the moment it was
	// filed under a different expense category.
	if oldKind != c.Kind {
		if c.Kind == KindExpense {
			t.AmountCents = -abs(t.AmountCents)
		} else {
			t.AmountCents = abs(t.AmountCents)
		}
	}
	return t, was, nil
}
