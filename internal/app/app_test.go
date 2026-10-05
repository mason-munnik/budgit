package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mason-munnik/budgit/internal/store"
)

// seeded returns an App on a temp file holding one account and one category.
func seeded(t *testing.T) (*App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.json")
	db, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAccount(db, "Checking", "checking", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCategory(db, "Groceries", store.KindExpense); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	return New(path), path
}

func TestAddTransactionReturnsFreshDashboard(t *testing.T) {
	a, path := seeded(t)
	d, err := a.AddTransaction(Request{
		Date: "2026-09-04", Account: "Checking", Category: "Groceries",
		Description: "Trader Joes", Amount: "84.31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Month != "2026-09" {
		t.Errorf("month %q, want the new transaction's month", d.Month)
	}
	if len(d.Transactions) != 1 || d.Transactions[0].AmountCents != -8431 {
		t.Fatalf("transactions %+v, want one -8431 expense", d.Transactions)
	}
	// And it is on disk, not just in the answer.
	db, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Transactions) != 1 {
		t.Errorf("%d transactions saved, want 1", len(db.Transactions))
	}
}

// Validation lives in store, so it comes with the app untouched: a bad write
// errors and leaves the file exactly as it was.
func TestRejectedWriteSavesNothing(t *testing.T) {
	a, path := seeded(t)
	before, _ := os.ReadFile(path)
	cases := []Request{
		{Account: "Checking", Category: "Groceries"},                 // no amount
		{Account: "Checking", Category: "Nope", Amount: "5"},         // unknown category
		{Account: "Checking", Category: "Groceries", Amount: "1,50"}, // comma cents
		{Date: "2026-13-01", Account: "Checking", Amount: "5"},       // bad date
	}
	for _, req := range cases {
		if _, err := a.AddTransaction(req); err == nil {
			t.Errorf("%+v accepted", req)
		}
	}
	if _, err := a.EditTransaction(Request{ID: 1}); err == nil {
		t.Error("edit with nothing to change accepted")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("rejected writes changed the file:\n%s", after)
	}
}

// Another process saving between our Load and our Save is refused, and its
// write survives.
func TestWriteConflict(t *testing.T) {
	a, path := seeded(t)
	_, err := a.write(Request{}, func(db *store.DB) error {
		other, _ := store.Load(path)
		if _, err := store.AddCategory(other, "Rent", store.KindExpense); err != nil {
			t.Fatal(err)
		}
		if err := other.Save(); err != nil {
			t.Fatal(err)
		}
		_, err := store.AddCategory(db, "Gas", store.KindExpense)
		return err
	})
	if !errors.Is(err, store.ErrChangedOnDisk) {
		t.Fatalf("err %v, want ErrChangedOnDisk", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "Rent") || strings.Contains(string(data), "Gas") {
		t.Errorf("file after conflict:\n%s", data)
	}
}

// Wails runs each call on its own goroutine. Without App.mu these would load
// the same file and all but one would fail with ErrChangedOnDisk.
func TestConcurrentCallsAllLand(t *testing.T) {
	a, path := seeded(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.AddTransaction(Request{
				Date: "2026-09-04", Account: "Checking", Category: "Groceries",
				Description: fmt.Sprintf("item %d", i), Amount: "1",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	db, _ := store.Load(path)
	if len(db.Transactions) != n {
		t.Errorf("%d transactions saved, want %d", len(db.Transactions), n)
	}
}

func TestDashboardDefaultsToNewestMonth(t *testing.T) {
	a, _ := seeded(t)
	for _, date := range []string{"2026-07-10", "2026-09-04"} {
		if _, err := a.AddTransaction(Request{Date: date, Category: "Groceries", Amount: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := a.Dashboard("")
	if err != nil {
		t.Fatal(err)
	}
	if d.Month != "2026-09" {
		t.Errorf("month %q, want 2026-09", d.Month)
	}
	if want := []string{"2026-09", "2026-07"}; fmt.Sprint(d.AvailableMonths) != fmt.Sprint(want) {
		t.Errorf("available %v, want %v", d.AvailableMonths, want)
	}
	if _, err := a.Dashboard("2026-9"); err == nil {
		t.Error("malformed month accepted")
	}
}

// The page sends lowercase keys and TrendQuery has no json tags; Wails decodes
// with encoding/json, which matches field names case-insensitively.
func TestTrendsFromPageShapedQuery(t *testing.T) {
	a, _ := seeded(t)
	if _, err := a.AddTransaction(Request{Date: "2026-09-04", Category: "Groceries", Amount: "10"}); err != nil {
		t.Fatal(err)
	}
	var q store.TrendQuery
	if err := json.Unmarshal([]byte(`{"period":"month","compare":"none","count":3,"end":"2026-09-30"}`), &q); err != nil {
		t.Fatal(err)
	}
	res, err := a.Trends(q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Period != "month" || res.Count != 3 || res.Compare != "none" || res.End != "2026-09-30" {
		t.Errorf("got %+v, want the page's query echoed back", res)
	}
	if _, err := a.Trends(store.TrendQuery{Period: "month", Count: -1}); err == nil {
		t.Error("negative count accepted")
	}
	if _, err := a.Trends(store.TrendQuery{Period: "fortnight"}); err == nil {
		t.Error("unknown period accepted")
	}
}
