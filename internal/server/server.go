package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mason-munnik/budgit/internal/store"
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
	Month           string             `json:"month"`
	GeneratedAt     string             `json:"generated_at"`
	Accounts        []accountView      `json:"accounts"`
	Categories      []store.Category   `json:"categories"`
	Report          store.Report       `json:"report"`
	Transactions    []txnView          `json:"transactions"`
	Trend           []store.MonthTotal `json:"trend"`
	AvailableMonths []string           `json:"available_months"`
	DataFile        string             `json:"data_file"`
	Rules           []ruleView         `json:"rules"`
}

type ruleView struct {
	ID       int    `json:"id"`
	Match    string `json:"match"`
	Category string `json:"category"`
}

// Run serves the dashboard for the data file at path until the listener fails.
// addr must be a loopback address, unless BUDGIT_ALLOW_REMOTE and BUDGIT_TOKEN
// are both set.
func Run(path, addr string) error {
	// Fail loudly rather than silently serving unauthenticated finances to the LAN.
	token, err := checkLoopback(addr)
	if err != nil {
		return err
	}
	// Surface a broken/missing data file now instead of on the first request.
	if _, err := store.Load(path); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		// Read under the same lock as writes, so a GET never lands between a
		// mutation and its save.
		dbMu.Lock()
		defer dbMu.Unlock()

		db, err := store.Load(path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		month := r.URL.Query().Get("month")
		if month == "" {
			month = latestMonth(db)
		}
		m, err := store.ValidateMonth(month)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, buildDashboard(db, m, path))
	})

	mux.HandleFunc("/api/trends", trendsHandler(path))

	// Every mutation the dashboard can perform. Each one answers with a freshly
	// built dashboard so the page re-renders from a single round trip.
	mux.HandleFunc("/api/txn/add", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.AddTransaction(db, req.Date, req.Account, req.Category, req.Description, req.Amount)
		return err
	}))
	mux.HandleFunc("/api/txn/delete", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.DeleteTransaction(db, req.ID)
		return err
	}))
	mux.HandleFunc("/api/txn/categorize", writeHandler(path, func(db *store.DB, req writeRequest) error {
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
	}))
	mux.HandleFunc("/api/txn/edit", writeHandler(path, func(db *store.DB, req writeRequest) error {
		if req.Edit == nil {
			return fmt.Errorf("nothing to change")
		}
		_, err := store.EditTransaction(db, req.ID, *req.Edit)
		return err
	}))
	mux.HandleFunc("/api/account/add", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.AddAccount(db, req.Name, req.Kind, req.Amount)
		return err
	}))
	mux.HandleFunc("/api/account/rename", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, _, err := store.RenameAccount(db, req.Account, req.Name)
		return err
	}))
	mux.HandleFunc("/api/account/delete", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.DeleteAccount(db, req.Account)
		return err
	}))
	mux.HandleFunc("/api/category/rename", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, _, err := store.RenameCategory(db, req.Category, req.Name)
		return err
	}))
	mux.HandleFunc("/api/category/delete", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, _, err := store.DeleteCategory(db, req.Category)
		return err
	}))
	mux.HandleFunc("/api/rule/add", writeHandler(path, func(db *store.DB, req writeRequest) error {
		if _, err := store.AddRule(db, req.Match, req.Category); err != nil {
			return err
		}
		// Applied at once: it only ever fills in uncategorized rows.
		store.ApplyRules(db)
		return nil
	}))
	mux.HandleFunc("/api/rule/delete", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.DeleteRule(db, req.ID)
		return err
	}))
	mux.HandleFunc("/api/account/balance", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, _, err := store.SetAccountBalance(db, req.Account, req.Amount)
		return err
	}))
	mux.HandleFunc("/api/category/add", writeHandler(path, func(db *store.DB, req writeRequest) error {
		_, err := store.AddCategory(db, req.Name, req.Kind)
		return err
	}))
	mux.HandleFunc("/api/budget/set", writeHandler(path, func(db *store.DB, req writeRequest) error {
		// The budget lands on the month the page is showing, which is the same
		// month the response is rebuilt for.
		_, _, _, err := store.SetCategoryBudget(db, req.Category, req.Month, req.Amount)
		return err
	}))

	// Serve the embedded web/ directory at the root.
	content, err := iofs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(content)))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w", addr, err)
	}
	fmt.Printf("budgit dashboard: http://%s\n", addr)
	fmt.Printf("data file: %s\n", path)
	fmt.Println("press ctrl-c to stop")

	// Requests are a few KB of JSON against a local file; nothing legitimate
	// needs longer than these, and they stop a stuck client pinning a connection.
	srv := &http.Server{
		Handler:           guard(addr, token, mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return srv.Serve(ln)
}

func trendsHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeErr(w, http.StatusMethodNotAllowed, "use GET")
			return
		}
		qv := r.URL.Query()
		q := store.TrendQuery{Period: qv.Get("period"), Within: qv.Get("within"), End: qv.Get("end"), Compare: qv.Get("compare")}
		if c := qv.Get("count"); c != "" {
			n, err := strconv.Atoi(c)
			if err != nil || n < 1 {
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("count %q must be a positive number", c))
				return
			}
			q.Count = n
		}

		dbMu.Lock()
		defer dbMu.Unlock()
		db, err := store.Load(path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := store.BuildTrends(db, q, store.Today())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	}
}

func buildDashboard(db *store.DB, month, path string) dashboard {
	d := dashboard{
		Month:       month,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Report:      store.BuildReport(db, month),
		Trend:       store.Trend(db, store.PrevMonths(month, 6)),
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
	d.Categories = append([]store.Category{}, db.Categories...)

	db.SortTransactions()
	for _, t := range db.Transactions {
		if store.MonthOf(t.Date) != month {
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

	d.Rules = []ruleView{}
	for _, r := range db.Rules {
		d.Rules = append(d.Rules, ruleView{r.ID, r.Match, db.CategoryName(r.CategoryID)})
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

// writeRequest is the one envelope every mutating endpoint accepts; an endpoint
// simply ignores the fields it has no use for. Amounts stay strings all the way
// to ParseMoney, so "$1,299", "84.31" and "+24.99" mean the same thing here as
// they do on the command line.
type writeRequest struct {
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

// writeHandler wraps one mutation with the checks every write shares: POST only,
// same-origin only, then load -> apply -> save under the lock.
func writeHandler(path string, apply func(*store.DB, writeRequest) error) http.HandlerFunc {
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

		db, err := store.Load(path)
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
			code := http.StatusInternalServerError
			if errors.Is(err, store.ErrChangedOnDisk) {
				code = http.StatusConflict
			}
			writeErr(w, code, err.Error())
			return
		}

		month := req.Month
		if month == "" {
			month = latestMonth(db)
		}
		m, err := store.ValidateMonth(month)
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
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg}) // headers are sent; nothing left to report to
}

// guard checks Host on every route, which stops DNS rebinding, and sets
// headers that forbid framing and third-party loads. A non-empty token is
// demanded as the HTTP Basic password on every request; the browser asks for it
// once and then sends it with each fetch by itself.
func guard(addr, token string, next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		allowed[strings.ToLower(host)] = true // an explicit BUDGIT_ALLOW_REMOTE address
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[strings.ToLower(strings.Trim(host, "[]"))] {
			writeErr(w, http.StatusForbidden, "unexpected Host "+r.Host+"; open the dashboard at http://"+addr)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if token != "" {
			_, pass, _ := r.BasicAuth()
			if subtle.ConstantTimeCompare([]byte(pass), []byte(token)) != 1 {
				h.Set("WWW-Authenticate", `Basic realm="budgit", charset="UTF-8"`)
				writeErr(w, http.StatusUnauthorized, "enter BUDGIT_TOKEN as the password (any username)")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// minTokenLen keeps BUDGIT_TOKEN out of guessing range for anyone on the LAN.
const minTokenLen = 16

// checkLoopback accepts a loopback addr, or any other addr when the user has
// opted in with BUDGIT_ALLOW_REMOTE=1 and set a BUDGIT_TOKEN, which it returns.
// Loopback needs no token: nothing else on the network can reach it.
func checkLoopback(addr string) (token string, err error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("--addr %q must be host:port, e.g. localhost:8080", addr)
	}
	if host == "" {
		return "", fmt.Errorf("--addr %q binds every interface; budgit has no auth, use localhost:8080", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return "", nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return "", nil
	}
	if os.Getenv("BUDGIT_ALLOW_REMOTE") != "1" {
		return "", fmt.Errorf("--addr %q is not loopback; budgit serves your finances with no auth.\n"+
			"       Use localhost:8080, or set BUDGIT_ALLOW_REMOTE=1 and BUDGIT_TOKEN if you really mean it", addr)
	}
	token = os.Getenv("BUDGIT_TOKEN")
	if len(token) < minTokenLen {
		return "", fmt.Errorf("BUDGIT_ALLOW_REMOTE needs BUDGIT_TOKEN set to at least %d characters;\n"+
			"       the dashboard asks for it as a password. Try: export BUDGIT_TOKEN=$(openssl rand -hex 16)", minTokenLen)
	}
	fmt.Fprintf(os.Stderr, "warning: serving on %s over plain HTTP; the token crosses the network unencrypted\n", addr)
	return token, nil
}
