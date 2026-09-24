package main

import (
	"embed"
	"encoding/json"
	"fmt"
	iofs "io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// dbMu serializes the load -> mutate -> save sequence. Two overlapping writes
// would otherwise each read the file, apply their own change, and write back —
// and whichever saved last would silently erase the other.
var dbMu sync.Mutex

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
	Categories      []Category    `json:"categories"`
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
		// Read under the same lock as writes, so a GET never lands between a
		// mutation and its save.
		dbMu.Lock()
		defer dbMu.Unlock()

		db, err := Load(path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		month := r.URL.Query().Get("month")
		if month == "" {
			month = latestMonth(db)
		}
		m, err := ValidateMonth(month)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, buildDashboard(db, m, path))
	})

	// Every mutation the dashboard can perform. Each one answers with a freshly
	// built dashboard so the page re-renders from a single round trip.
	mux.HandleFunc("/api/txn/add", writeHandler(path, func(db *DB, req writeRequest) error {
		_, err := AddTransaction(db, req.Date, req.Account, req.Category, req.Description, req.Amount)
		return err
	}))
	mux.HandleFunc("/api/txn/delete", writeHandler(path, func(db *DB, req writeRequest) error {
		_, err := DeleteTransaction(db, req.ID)
		return err
	}))
	mux.HandleFunc("/api/txn/categorize", writeHandler(path, func(db *DB, req writeRequest) error {
		_, _, err := CategorizeTransaction(db, req.ID, req.Category)
		return err
	}))
	mux.HandleFunc("/api/account/balance", writeHandler(path, func(db *DB, req writeRequest) error {
		_, _, err := SetAccountBalance(db, req.Account, req.Amount)
		return err
	}))
	mux.HandleFunc("/api/category/add", writeHandler(path, func(db *DB, req writeRequest) error {
		_, err := AddCategory(db, req.Name, req.Kind)
		return err
	}))
	mux.HandleFunc("/api/budget/set", writeHandler(path, func(db *DB, req writeRequest) error {
		// The budget lands on the month the page is showing, which is the same
		// month the response is rebuilt for.
		_, _, _, err := SetCategoryBudget(db, req.Category, req.Month, req.Amount)
		return err
	}))

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

	for _, a := range db.Accounts {
		d.Accounts = append(d.Accounts, accountView{a.ID, a.Name, a.Type, db.AccountBalance(a.ID)})
	}
	if d.Accounts == nil {
		d.Accounts = []accountView{}
	}

	// The report only carries categories with a budget or activity; the entry
	// form needs every category that exists.
	d.Categories = append([]Category{}, db.Categories...)

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

// writeRequest is the one envelope every mutating endpoint accepts; an endpoint
// simply ignores the fields it has no use for. Amounts stay strings all the way
// to ParseMoney, so "$1,299", "84.31" and "+24.99" mean the same thing here as
// they do on the command line.
type writeRequest struct {
	Month       string `json:"month"`
	Date        string `json:"date"`
	Account     string `json:"account"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	ID          int    `json:"id"`
}

// writeHandler wraps one mutation with the checks every write shares: POST only,
// same-origin only, then load -> apply -> save under the lock.
func writeHandler(path string, apply func(*DB, writeRequest) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeErr(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		if err := checkSameOrigin(r); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}

		var req writeRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "cannot read request: "+err.Error())
			return
		}

		dbMu.Lock()
		defer dbMu.Unlock()

		db, err := Load(path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// A rejected mutation leaves db untouched and unsaved, so a bad
		// category name costs nothing.
		if err := apply(db, req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := db.Save(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		month := req.Month
		if month == "" {
			month = latestMonth(db)
		}
		m, err := ValidateMonth(month)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, buildDashboard(db, m, path))
	}
}

// checkSameOrigin stops another site open in the same browser from driving this
// server. budgit has no authentication: before writes existed the worst a hostile
// page could do was fail to read the JSON, but a mutation endpoint it could reach
// would let it edit your finances outright.
func checkSameOrigin(r *http.Request) error {
	// A cross-origin <form> or no-preflight fetch can only send the form
	// encodings or text/plain. Insisting on JSON forces a CORS preflight that
	// this server never answers.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return fmt.Errorf("Content-Type must be application/json")
	}
	// Sent by current browsers; "none" means the user drove it directly.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return fmt.Errorf("cross-origin request refused")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return fmt.Errorf("cross-origin request refused")
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writeErr answers in JSON so the dashboard can show the same message the CLI
// would have printed, rather than a wall of plain text.
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
