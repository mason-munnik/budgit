package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mason-munnik/budgit/internal/store"
)

func TestGuardRejectsForeignHost(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := guard("localhost:8080", "", ok)
	cases := []struct {
		host string
		want int
	}{
		{"localhost:8080", 200},
		{"127.0.0.1:8080", 200},
		{"[::1]:8080", 200},
		{"LOCALHOST:8080", 200},
		// DNS rebinding: the attacker's name, resolved to loopback.
		{"evil.example:8080", 403},
		{"localhost.evil.example:8080", 403},
		{"", 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/dashboard", nil)
		r.Host = c.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("Host %q: %d, want %d", c.host, w.Code, c.want)
		}
		if c.want == 200 {
			for _, hdr := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
				if w.Header().Get(hdr) == "" {
					t.Errorf("Host %q: no %s header", c.host, hdr)
				}
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Error("CSP must forbid framing")
			}
		}
	}

	// An address chosen with BUDGIT_ALLOW_REMOTE is reachable by that name.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "192.168.1.5:8080"
	w := httptest.NewRecorder()
	guard("192.168.1.5:8080", "", ok).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("explicit remote address: %d, want 200", w.Code)
	}
}

// A conflicting write answers 409 and leaves the other write in place.
func TestWriteHandlerConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	db, _ := store.Load(path)
	mustCategory(t, db, "Groceries")
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	h := writeHandler(path, func(db *store.DB, req writeRequest) error {
		// Someone else saves between our Load and our Save.
		other, _ := store.Load(path)
		mustCategory(t, other, "Rent")
		if err := other.Save(); err != nil {
			t.Fatal(err)
		}
		_, err := store.AddCategory(db, req.Name, "")
		return err
	})
	r := httptest.NewRequest("POST", "/api/category/add", strings.NewReader(`{"name":"Gas"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "Rent") || strings.Contains(string(data), "Gas") {
		t.Errorf("file after conflict:\n%s", data)
	}
}

func TestCheckLoopback(t *testing.T) {
	t.Setenv("BUDGIT_ALLOW_REMOTE", "")
	t.Setenv("BUDGIT_TOKEN", "")
	for _, ok := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:9000"} {
		if token, err := checkLoopback(ok); err != nil || token != "" {
			t.Errorf("checkLoopback(%q) = %q, %v; want no token, no error", ok, token, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:8080", "192.168.1.5:8080", ":8080"} {
		if _, err := checkLoopback(bad); err == nil {
			t.Errorf("checkLoopback(%q) should have been rejected", bad)
		}
	}
}

// Opting in to a LAN address is not enough on its own: it takes a token too.
func TestCheckLoopbackRemoteNeedsToken(t *testing.T) {
	t.Setenv("BUDGIT_ALLOW_REMOTE", "1")
	for _, weak := range []string{"", "short"} {
		t.Setenv("BUDGIT_TOKEN", weak)
		if _, err := checkLoopback("192.168.1.5:8080"); err == nil {
			t.Errorf("token %q accepted; want refused", weak)
		}
	}
	const good = "0123456789abcdef0123"
	t.Setenv("BUDGIT_TOKEN", good)
	if token, err := checkLoopback("192.168.1.5:8080"); err != nil || token != good {
		t.Errorf("got %q, %v; want the token back", token, err)
	}
	// Binding every interface stays refused even with a token.
	if _, err := checkLoopback(":8080"); err == nil {
		t.Error(":8080 accepted")
	}
}

func TestGuardToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := guard("192.168.1.5:8080", "0123456789abcdef", ok)
	cases := []struct {
		name, pass string
		auth       bool
		want       int
	}{
		{"no credentials", "", false, 401},
		{"wrong password", "0123456789abcdeX", true, 401},
		{"prefix of token", "0123456789", true, 401},
		{"right password", "0123456789abcdef", true, 200},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/dashboard", nil)
		r.Host = "192.168.1.5:8080"
		if c.auth {
			r.SetBasicAuth("anyone", c.pass)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
		if c.want == 401 {
			if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") {
				t.Errorf("%s: no Basic challenge, so the browser would never prompt", c.name)
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s: 401 sent without security headers", c.name)
			}
		}
	}
	// The Host check still comes first: a rebinding page with the password is refused.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil.example:8080"
	r.SetBasicAuth("x", "0123456789abcdef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("foreign Host with token: %d, want 403", w.Code)
	}
}

func mustCategory(t *testing.T, db *store.DB, name string) {
	t.Helper()
	if _, err := store.AddCategory(db, name, store.KindExpense); err != nil {
		t.Fatal(err)
	}
}

func TestTrendsEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	h := trendsHandler(path)
	cases := []struct {
		method, query string
		want          int
	}{
		{"GET", "period=month", 200},
		{"GET", "period=bogus", 400},
		{"GET", "period=month&count=abc", 400},
		{"GET", "period=month&count=0", 400},
		{"POST", "period=month", 405},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, "/api/trends?"+c.query, nil))
		if w.Code != c.want {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.query, w.Code, c.want, w.Body)
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: not JSON", c.method, c.query)
		}
	}
}
