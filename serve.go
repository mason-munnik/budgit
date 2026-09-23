package main

import (
	"embed"
	"encoding/json"
	"fmt"
	iofs "io/fs"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// txnView is a transaction with its foreign keys resolved, so the browser
// never has to join anything.
type txnView struct {
	ID          int    `json:"id"`
	Date        string `json:"date"`
	Account     string `json:"account"`
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	AmountCents int64  `json:"amount_cents"`
}

type accountView struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	BalanceCents int64  `json:"balance_cents"`
}

type dashboard struct {
	Month           string        `json:"month"`
	GeneratedAt     string        `json:"generated_at"`
	Accounts        []accountView `json:"accounts"`
	Report          Report        `json:"report"`
	Transactions    []txnView     `json:"transactions"`
	Trend           []MonthTotal  `json:"trend"`
	AvailableMonths []string      `json:"available_months"`
	DataFile        string        `json:"data_file"`
}

func cmdServe(args []string) error {
	var path string
	fs := newFS("serve", &path)
	addr := fs.String("addr", "localhost:8080", "address to bind (keep it on loopback)")
	fs.Parse(args)

	// Fail loudly rather than silently serving unauthenticated finances to the LAN.
	if err := checkLoopback(*addr); err != nil {
		return err
	}
	// Surface a broken/missing data file now instead of on the first request.
	if _, err := Load(path); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		db, err := Load(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		month := r.URL.Query().Get("month")
		if month == "" {
			month = latestMonth(db)
		}
		m, err := ValidateMonth(month)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(buildDashboard(db, m, path)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	// Serve the embedded web/ directory at the root.
	content, err := iofs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(content)))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w", *addr, err)
	}
	fmt.Printf("budgit dashboard: http://%s\n", *addr)
	fmt.Printf("data file: %s\n", path)
	fmt.Println("press ctrl-c to stop")

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

func buildDashboard(db *DB, month, path string) dashboard {
	d := dashboard{
		Month:       month,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Report:      BuildReport(db, month),
		Trend:       Trend(db, PrevMonths(month, 6)),
		DataFile:    path,
	}

	bal := map[int]int64{}
	for _, t := range db.Transactions {
		bal[t.AccountID] += t.AmountCents
	}
	for _, a := range db.Accounts {
		d.Accounts = append(d.Accounts, accountView{a.ID, a.Name, a.Type, bal[a.ID]})
	}
	if d.Accounts == nil {
		d.Accounts = []accountView{}
	}

	db.SortTransactions()
	for _, t := range db.Transactions {
		if MonthOf(t.Date) != month {
			continue
		}
		kind := ""
		if c := db.CategoryByID(t.CategoryID); c != nil {
			kind = c.Kind
		}
		d.Transactions = append(d.Transactions, txnView{
			ID: t.ID, Date: t.Date, Account: db.AccountName(t.AccountID),
			Category: db.CategoryName(t.CategoryID), Kind: kind,
			Description: t.Description, AmountCents: t.AmountCents,
		})
	}
	if d.Transactions == nil {
		d.Transactions = []txnView{}
	}

	d.AvailableMonths = availableMonths(db, month)
	return d
}

// availableMonths lists every month with data, plus the one being viewed,
// newest first — so the picker never omits the current selection.
func availableMonths(db *DB, current string) []string {
	seen := map[string]bool{current: true}
	for _, t := range db.Transactions {
		seen[MonthOf(t.Date)] = true
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
func latestMonth(db *DB) string {
	best := ""
	for _, t := range db.Transactions {
		if m := MonthOf(t.Date); m > best {
			best = m
		}
	}
	for _, b := range db.Budgets {
		if b.Month > best {
			best = b.Month
		}
	}
	if best == "" {
		return CurrentMonth()
	}
	return best
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %q must be host:port, e.g. localhost:8080", addr)
	}
	if host == "" {
		return fmt.Errorf("--addr %q binds every interface; budgit has no auth, use localhost:8080", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if os.Getenv("BUDGIT_ALLOW_REMOTE") == "1" {
		fmt.Fprintf(os.Stderr, "warning: serving on %s with no authentication\n", addr)
		return nil
	}
	return fmt.Errorf("--addr %q is not loopback; budgit serves your finances with no auth.\n"+
		"       Use localhost:8080, or set BUDGIT_ALLOW_REMOTE=1 if you really mean it", addr)
}
