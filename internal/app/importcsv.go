package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mason-munnik/budgit/internal/csvimport"
	"github.com/mason-munnik/budgit/internal/store"
)

// maxCSVBytes is far beyond years of bank statements; it stops a mis-dropped
// video or disk image from being read into memory and parsed.
const maxCSVBytes = 10 << 20

// Startup is passed to Wails as OnStartup. It is a function rather than a
// method so Wails does not bind it into the page.
func Startup(a *App) func(context.Context) {
	return func(ctx context.Context) { a.ctx = ctx }
}

// ChooseCSV shows the Finder "Open" panel and returns the chosen path, or ""
// if the user cancelled.
func (a *App) ChooseCSV() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose a bank statement",
		Filters: []runtime.FileFilter{{DisplayName: "CSV files (*.csv)", Pattern: "*.csv"}},
	})
}

// ImportRequest is what the import panel sends for both the preview and the
// import itself.
type ImportRequest struct {
	Path    string `json:"path"`
	Account string `json:"account"`
	// Sign answers a sign conflict: "" (none seen yet), "flip" or "keep".
	Sign string `json:"sign"`
	// Fingerprint is the SHA-256 PreviewImport returned. ImportCSV refuses a
	// file whose bytes no longer hash to it.
	Fingerprint string `json:"fingerprint"`
	Month       string `json:"month"` // the month the dashboard is showing
}

type ImportRow struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Category    string `json:"category"`
	AmountCents int64  `json:"amount_cents"`
	ExistingID  int    `json:"existing_id,omitempty"` // suspects only
}

// ImportPreview is everything the panel shows before anything is saved.
type ImportPreview struct {
	Fingerprint string `json:"fingerprint"`
	FileName    string `json:"file_name"`
	Account     string `json:"account"`

	// SignConflict means nothing below is final: the panel shows AsIs and
	// Flipped and asks which looks right, then previews again with Sign set.
	SignConflict bool        `json:"sign_conflict"`
	AsIs         []ImportRow `json:"as_is,omitempty"`
	Flipped      []ImportRow `json:"flipped,omitempty"`

	Rows        int `json:"rows"`
	New         int `json:"new"`         // transactions that would be added
	Matched     int `json:"matched"`     // rows that claim one you typed by hand
	Known       int `json:"known"`       // already imported (same bank id); skipped
	Pending     int `json:"pending"`     // not yet posted; skipped
	Skipped     int `json:"skipped"`     // blank, total and zero-amount lines
	Categorized int `json:"categorized"` // of New, how many got a category

	Sample   []ImportRow `json:"sample"`   // the first few new transactions
	Suspects []ImportRow `json:"suspects"` // possible duplicates, imported anyway
}

// ImportOutcome answers ImportCSV. Changed means the file is not the one that
// was previewed: nothing was saved and Preview holds a fresh look at it.
type ImportOutcome struct {
	Changed   bool           `json:"changed"`
	Preview   *ImportPreview `json:"preview,omitempty"`
	Imported  int            `json:"imported"`
	Matched   int            `json:"matched"`
	Dashboard *Dashboard     `json:"dashboard,omitempty"`
}

// readCSV reads the file once; the caller hashes and parses these same bytes,
// so what was fingerprinted is exactly what gets imported.
func readCSV(path string) (data []byte, fingerprint string, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", fmt.Errorf("choose a CSV file first")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !fi.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s is not a file", filepath.Base(path))
	}
	if fi.Size() > maxCSVBytes {
		return nil, "", fmt.Errorf("%s is %d MB; a bank statement is never more than %d MB",
			filepath.Base(path), fi.Size()>>20, maxCSVBytes>>20)
	}
	// #nosec G304 -- a path the user picked in the Open panel or dropped on the
	// window; it is only parsed as CSV, never served or executed.
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxCSVBytes { // grew after the Stat
		return nil, "", fmt.Errorf("%s is larger than %d MB", filepath.Base(path), maxCSVBytes>>20)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func signOptions(sign string) (csvimport.Options, error) {
	switch sign {
	case "":
		return csvimport.Options{}, nil
	case "flip":
		return csvimport.Options{Invert: true}, nil
	case "keep":
		return csvimport.Options{NoInvert: true}, nil
	}
	return csvimport.Options{}, fmt.Errorf("unknown sign choice %q", sign)
}

// importAccount resolves the panel's account, defaulting to the only one.
func importAccount(db *store.DB, ref string) (*store.Account, error) {
	if strings.TrimSpace(ref) == "" {
		if len(db.Accounts) != 1 {
			return nil, fmt.Errorf("choose which account this statement belongs to")
		}
		return &db.Accounts[0], nil
	}
	return db.FindAccount(ref)
}

// PreviewImport reads the file and reports what importing it would do. It
// never saves.
func (a *App) PreviewImport(req ImportRequest) (ImportPreview, error) {
	data, fp, err := readCSV(req.Path)
	if err != nil {
		return ImportPreview{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	db, err := store.Load(a.path)
	if err != nil {
		return ImportPreview{}, err
	}
	return preview(db, req, data, fp)
}

func preview(db *store.DB, req ImportRequest, data []byte, fp string) (ImportPreview, error) {
	acct, err := importAccount(db, req.Account)
	if err != nil {
		return ImportPreview{}, err
	}
	opts, err := signOptions(req.Sign)
	if err != nil {
		return ImportPreview{}, err
	}
	p := ImportPreview{Fingerprint: fp, FileName: filepath.Base(req.Path), Account: acct.Name}

	opts.DryRun = true
	res, err := csvimport.ImportData(db, req.Path, data, acct.ID, opts)
	if errors.Is(err, csvimport.ErrSignConflict) {
		// Show the rows both ways and let the user say which looks right.
		keep, kerr := csvimport.ImportData(db, req.Path, data, acct.ID, csvimport.Options{DryRun: true, NoInvert: true})
		flip, ferr := csvimport.ImportData(db, req.Path, data, acct.ID, csvimport.Options{DryRun: true, Invert: true})
		if kerr != nil || ferr != nil {
			return ImportPreview{}, errors.Join(err, kerr, ferr)
		}
		p.SignConflict = true
		p.AsIs, p.Flipped = previewRows(keep), previewRows(flip)
		return p, nil
	}
	if err != nil {
		return ImportPreview{}, err
	}

	p.Rows, p.New, p.Matched, p.Known = res.Rows, res.Imported, res.Matched, res.Duplicates
	p.Pending, p.Skipped, p.Categorized = res.Pending, res.Junk+res.Zero, res.Categorized
	p.Sample = previewRows(res)
	p.Suspects = []ImportRow{}
	for _, s := range res.Suspects {
		p.Suspects = append(p.Suspects, ImportRow{Date: s.Date, Description: s.Desc, AmountCents: s.Cents, ExistingID: s.ExistingID})
	}
	return p, nil
}

func previewRows(res *csvimport.Result) []ImportRow {
	rows := []ImportRow{}
	for _, r := range res.Preview {
		rows = append(rows, ImportRow{Date: r.Date, Description: r.Desc, Category: r.Category, AmountCents: r.Cents})
	}
	return rows
}

// ImportCSV imports the previewed file, provided its bytes still hash to the
// previewed fingerprint. If they don't, it saves nothing and answers with a
// fresh preview instead.
func (a *App) ImportCSV(req ImportRequest) (ImportOutcome, error) {
	data, fp, err := readCSV(req.Path)
	if err != nil {
		return ImportOutcome{}, err
	}
	opts, err := signOptions(req.Sign)
	if err != nil {
		return ImportOutcome{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	db, err := store.Load(a.path)
	if err != nil {
		return ImportOutcome{}, err
	}
	if fp != req.Fingerprint {
		p, err := preview(db, req, data, fp)
		if err != nil {
			return ImportOutcome{}, err
		}
		return ImportOutcome{Changed: true, Preview: &p}, nil
	}
	acct, err := importAccount(db, req.Account)
	if err != nil {
		return ImportOutcome{}, err
	}
	res, err := csvimport.ImportData(db, req.Path, data, acct.ID, opts)
	if err != nil {
		return ImportOutcome{}, err
	}
	if res.Imported > 0 || res.Matched > 0 {
		// The undo snapshot must be the very bytes Load parsed; if another
		// process wrote in between, Save would refuse anyway.
		before, err := os.ReadFile(a.path)
		if err != nil {
			return ImportOutcome{}, err
		}
		if !bytes.Equal(store.Hash(before), db.Fingerprint()) {
			return ImportOutcome{}, store.ErrChangedOnDisk
		}
		if err := db.Save(); err != nil {
			return ImportOutcome{}, err
		}
		a.undo = &importUndo{
			before: before,
			after:  db.Fingerprint(),
			info:   UndoInfo{FileName: filepath.Base(req.Path), Imported: res.Imported, Matched: res.Matched},
		}
	}
	d, err := a.view(db, req.Month)
	if err != nil {
		return ImportOutcome{}, err
	}
	return ImportOutcome{Imported: res.Imported, Matched: res.Matched, Dashboard: &d}, nil
}
