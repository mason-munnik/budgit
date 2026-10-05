// Package app is budgit's desktop front end. Wails binds App's exported methods
// into the window's JavaScript, so the page calls Go directly: no HTTP server,
// no listening port, nothing another program on the machine can reach.
package app

import (
	"context"
	"embed"
	"fmt"
	iofs "io/fs"
	"strings"
	"sync"

	"github.com/mason-munnik/budgit/internal/store"
)

//go:embed frontend
var frontendFS embed.FS

// Frontend is the page Wails shows in the window.
func Frontend() (iofs.FS, error) { return iofs.Sub(frontendFS, "frontend") }

// App owns the data file for one window.
type App struct {
	path string
	// ctx is the Wails runtime context, set by Startup; dialogs need it.
	ctx context.Context
	// mu serializes load -> mutate -> save. Wails runs each call from the page
	// on its own goroutine, and two overlapping calls would both load the same
	// file; the second save would then fail with ErrChangedOnDisk for no reason
	// the user could see.
	mu sync.Mutex
}

// New returns an App for the data file at path.
func New(path string) *App { return &App{path: path} }

// Request is the one shape every write accepts; a method ignores the fields it
// has no use for. Amounts stay strings all the way to ParseMoney, so "$1,299",
// "84.31" and "+24.99" mean the same thing here as on the command line.
type Request struct {
	Month       string         `json:"month"`
	Date        string         `json:"date"`
	Account     string         `json:"account"`
	Category    string         `json:"category"`
	Description string         `json:"description"`
	Amount      string         `json:"amount"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	ID          int            `json:"id"`
	Match       string         `json:"match"` // a rule's text; on categorize, also add that rule
	None        bool           `json:"none"`  // categorize: back to uncategorized
	Edit        *store.TxnEdit `json:"edit"`
}

// Dashboard reads the data file and builds the view for month, or for the
// newest month with data when month is empty.
func (a *App) Dashboard(month string) (Dashboard, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	db, err := store.Load(a.path)
	if err != nil {
		return Dashboard{}, err
	}
	return a.view(db, month)
}

// Trends builds the compare-periods card. A zero Count means the default.
func (a *App) Trends(q store.TrendQuery) (store.TrendsResult, error) {
	if q.Count < 0 {
		return store.TrendsResult{}, fmt.Errorf("count %d must be a positive number", q.Count)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	db, err := store.Load(a.path)
	if err != nil {
		return store.TrendsResult{}, err
	}
	return store.BuildTrends(db, q, store.Today())
}

// write is every mutation's shared path: load, apply, save, and answer with a
// fresh dashboard so the page redraws without a second call. A rejected
// mutation is never saved, so a bad category name costs nothing.
func (a *App) write(req Request, apply func(*store.DB) error) (Dashboard, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	db, err := store.Load(a.path)
	if err != nil {
		return Dashboard{}, err
	}
	if err := apply(db); err != nil {
		return Dashboard{}, err
	}
	if err := db.Save(); err != nil {
		return Dashboard{}, err
	}
	return a.view(db, req.Month)
}

func (a *App) view(db *store.DB, month string) (Dashboard, error) {
	if month == "" {
		month = latestMonth(db)
	}
	m, err := store.ValidateMonth(month)
	if err != nil {
		return Dashboard{}, err
	}
	return buildDashboard(db, m, a.path), nil
}

// ---- transactions ----

func (a *App) AddTransaction(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.AddTransaction(db, req.Date, req.Account, req.Category, req.Description, req.Amount)
		return err
	})
}

func (a *App) DeleteTransaction(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.DeleteTransaction(db, req.ID)
		return err
	})
}

func (a *App) CategorizeTransaction(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		if req.None {
			_, _, err := store.UncategorizeTransaction(db, req.ID)
			return err
		}
		if _, _, err := store.CategorizeTransaction(db, req.ID, req.Category); err != nil {
			return err
		}
		// match also adds a rule and applies it to other uncategorized rows.
		if strings.TrimSpace(req.Match) != "" {
			if _, err := store.AddRule(db, req.Match, req.Category); err != nil {
				return err
			}
			store.ApplyRules(db)
		}
		return nil
	})
}

func (a *App) EditTransaction(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		if req.Edit == nil {
			return fmt.Errorf("nothing to change")
		}
		_, err := store.EditTransaction(db, req.ID, *req.Edit)
		return err
	})
}

// ---- accounts ----

func (a *App) AddAccount(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.AddAccount(db, req.Name, req.Kind, req.Amount)
		return err
	})
}

func (a *App) RenameAccount(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, _, err := store.RenameAccount(db, req.Account, req.Name)
		return err
	})
}

func (a *App) DeleteAccount(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.DeleteAccount(db, req.Account)
		return err
	})
}

func (a *App) SetAccountBalance(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, _, err := store.SetAccountBalance(db, req.Account, req.Amount)
		return err
	})
}

// ---- categories ----

func (a *App) AddCategory(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.AddCategory(db, req.Name, req.Kind)
		return err
	})
}

func (a *App) RenameCategory(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, _, err := store.RenameCategory(db, req.Category, req.Name)
		return err
	})
}

func (a *App) DeleteCategory(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, _, err := store.DeleteCategory(db, req.Category)
		return err
	})
}

// ---- rules and budgets ----

func (a *App) AddRule(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		if _, err := store.AddRule(db, req.Match, req.Category); err != nil {
			return err
		}
		// Applied at once: it only ever fills in uncategorized rows.
		store.ApplyRules(db)
		return nil
	})
}

func (a *App) DeleteRule(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		_, err := store.DeleteRule(db, req.ID)
		return err
	})
}

func (a *App) SetBudget(req Request) (Dashboard, error) {
	return a.write(req, func(db *store.DB) error {
		// The budget lands on the month the page is showing, which is the same
		// month the dashboard is rebuilt for.
		_, _, _, err := store.SetCategoryBudget(db, req.Category, req.Month, req.Amount)
		return err
	})
}
