package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// importOptions carries every flag `budgit txn import` accepts. Zero values mean
// "work it out from the file".
type importOptions struct {
	DryRun     bool
	Invert     bool
	NoInvert   bool
	NoCategory bool
	Maps       []string          // "BANK LABEL=Budgit Category"
	Delimiter  string            // "," ";" "tab" ...
	Decimal    string            // "dot" | "comma"
	DateFormat string            // a Go time layout
	Cols       map[string]string // field -> header name or index
}

// errNoAmount marks a row with nothing to import, as opposed to one that is
// broken. Trailing "Totals" lines and blank separators land here.
var errNoAmount = errors.New("no amount")

// dateLayouts are tried in order. Only the unpadded "1/2/2006" is listed,
// because Go accepts both one- and two-digit fields for it: that one layout
// covers 9/22/2026 and 09/22/2026 alike.
var dateLayouts = []string{
	"2006-01-02",
	"1/2/2006",
	"1/2/06",
	"2006/1/2",
	"2-Jan-2006",
	"2 Jan 2006",
	"Jan 2, 2006",
	"2.1.2006",
}

// parseCSVDate normalises whatever the bank wrote into YYYY-MM-DD. Slashed
// dates are read month-first; 3/4/2026 is genuinely ambiguous and guessing the
// other way would be invisible, so --date-format is the way out.
func parseCSVDate(s, custom string) (string, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return "", nil
	}
	layouts := dateLayouts
	if strings.TrimSpace(custom) != "" {
		layouts = []string{custom}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, v); err == nil {
			return t.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("date %q is in a format budgit does not recognise (try --date-format)", s)
}

// normalizeAmount undoes the padding and accounting style banks add, and only
// that, before ParseMoney sees the value. It never rounds: real precision past
// cents is an error, because silently dropping it would change the number.
func normalizeAmount(s string, decimalSep byte) (string, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return "", nil
	}

	neg := false
	if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		neg = true // accounting negative: (37.44)
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	if strings.HasSuffix(v, "-") { // trailing sign, as mainframe exports write it
		neg = true
		v = strings.TrimSpace(strings.TrimSuffix(v, "-"))
	}
	if strings.HasPrefix(v, "-") {
		neg = !neg
		v = strings.TrimSpace(v[1:])
	}
	v = strings.TrimPrefix(v, "+")

	if decimalSep == ',' {
		// Decimal comma means the dot is the thousands separator.
		v = strings.ReplaceAll(v, ".", "")
		if i := strings.LastIndexByte(v, ','); i >= 0 {
			v = v[:i] + "." + v[i+1:]
		}
	}
	if v == "" {
		return "", nil
	}

	// Banks pad to 5 decimals; ParseMoney rejects anything past 2.
	if i := strings.IndexByte(v, '.'); i >= 0 {
		whole, frac := v[:i], v[i+1:]
		if len(frac) > 2 {
			if strings.Trim(frac[2:], "0") != "" {
				return "", fmt.Errorf("amount %q carries more precision than cents", s)
			}
			frac = frac[:2]
		}
		v = whole + "." + frac
	}
	if neg {
		v = "-" + v
	}
	return v, nil
}

// parseCents runs one cell through normalisation and the shared money parser.
// An empty cell is not an error; it reports ok=false.
func parseCents(raw string, decimalSep byte) (cents int64, ok bool, err error) {
	norm, err := normalizeAmount(raw, decimalSep)
	if err != nil {
		return 0, false, err
	}
	if norm == "" {
		return 0, false, nil
	}
	cents, _, err = ParseMoney(norm)
	if err != nil {
		return 0, false, err
	}
	return cents, true, nil
}

// rowAmount reads one row's signed cents according to the dialect. The file's
// own sign is authoritative: the importer never runs AddTransaction's
// category-direction inference, which would turn an unsigned paycheck negative.
func (d *dialect) rowAmount(row []string) (cents int64, assumedOut bool, err error) {
	switch d.Mode {
	case modePair:
		debit, hasDebit, err := parseCents(cell(row, d.Debit), d.DecimalSep)
		if err != nil {
			return 0, false, err
		}
		credit, hasCredit, err := parseCents(cell(row, d.Credit), d.DecimalSep)
		if err != nil {
			return 0, false, err
		}
		// A zero in the unused column is how many banks fill the pair.
		hasDebit = hasDebit && debit != 0
		hasCredit = hasCredit && credit != 0
		switch {
		case hasDebit && hasCredit:
			return 0, false, fmt.Errorf("row has both a debit (%s) and a credit (%s)",
				cell(row, d.Debit), cell(row, d.Credit))
		case hasDebit:
			return -abs(debit), false, nil
		case hasCredit:
			return abs(credit), false, nil
		}
		return 0, false, errNoAmount

	case modeTyped:
		amount, ok, err := parseCents(cell(row, d.Amount), d.DecimalSep)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return 0, false, errNoAmount
		}
		switch directionWord(cell(row, d.Type)) {
		case 1:
			return abs(amount), false, nil
		case -1:
			return -abs(amount), false, nil
		}
		// An unrecognised type word is assumed to be money going out — almost
		// everything on a statement is — and counted so the summary says so.
		return -abs(amount), true, nil

	default:
		amount, ok, err := parseCents(cell(row, d.Amount), d.DecimalSep)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return 0, false, errNoAmount
		}
		if d.Invert {
			amount = -amount
		}
		return amount, false, nil
	}
}

// bankCategoryAliases maps labels banks commonly use onto budgit's own names.
// Keys are lowercased. A target that does not exist as a category leaves the row
// uncategorized — nothing here ever creates a category.
var bankCategoryAliases = map[string]string{
	"gasoline/fuel":        "Gas",
	"gas & fuel":           "Gas",
	"gas/fuel":             "Gas",
	"fuel":                 "Gas",
	"restaurants & dining": "Eating-Out",
	"restaurants":          "Eating-Out",
	"restaurant":           "Eating-Out",
	"dining":               "Eating-Out",
	"food & dining":        "Eating-Out",
	"paychecks/salary":     "Paycheck",
	"paycheck":             "Paycheck",
	"payroll":              "Paycheck",
	"salary":               "Paycheck",
	"groceries":            "Groceries",
	"grocery":              "Groceries",
	"entertainment":        "Entertainment",
	"shopping":             "Shopping",
	"merchandise":          "Shopping",
	"utilities":            "Utilities",
	"clothing":             "Clothing",
	"parking":              "Parking/Tolls",
	"tolls":                "Parking/Tolls",
}

// categoryResolver turns a bank's category label into a budgit category id.
type categoryResolver struct {
	byName   map[string]int    // lowercased budgit category name -> id
	user     map[string]string // lowercased bank label -> budgit category name
	disabled bool
}

func newCategoryResolver(db *DB, opts importOptions) (*categoryResolver, error) {
	r := &categoryResolver{
		byName:   make(map[string]int, len(db.Categories)),
		user:     map[string]string{},
		disabled: opts.NoCategory,
	}
	for _, c := range db.Categories {
		r.byName[normHeader(c.Name)] = c.ID
	}
	for _, m := range opts.Maps {
		from, to, found := strings.Cut(m, "=")
		if !found || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("--map wants \"BANK LABEL=Budgit Category\", got %q", m)
		}
		// Catch a typo now rather than silently leaving rows uncategorized.
		if _, ok := r.byName[normHeader(to)]; !ok {
			return nil, fmt.Errorf("--map %q: no category named %q (try: budgit category list)", m, strings.TrimSpace(to))
		}
		r.user[normHeader(from)] = strings.TrimSpace(to)
	}
	return r, nil
}

// resolve returns a category id, or 0 for uncategorized. Deliberately not
// FindCategory: its unique-substring matching is right for one hand-typed
// --category and far too loose to run unattended over a whole statement.
func (r *categoryResolver) resolve(label string) int {
	if r.disabled {
		return 0
	}
	key := normHeader(label)
	if key == "" {
		return 0
	}
	if to, ok := r.user[key]; ok {
		return r.byName[normHeader(to)]
	}
	if id, ok := r.byName[key]; ok {
		return id
	}
	if to, ok := bankCategoryAliases[key]; ok {
		return r.byName[normHeader(to)] // 0 when that category does not exist
	}
	return 0
}

// suspectRow is a row budgit imported but thinks you may already have. It does
// not skip it: two identical purchases on one day are real, and only you can
// tell them apart from an export that renumbered its own ids.
type suspectRow struct {
	Date       string
	Desc       string
	Cents      int64
	ExistingID int
}

type previewRow struct {
	Date     string
	Desc     string
	Category string
	Cents    int64
}

type importResult struct {
	Path     string
	Dialect  *dialect
	Rows     int
	Imported int

	Pending     int
	Junk        int
	Zero        int
	Duplicates  int
	Matched     int
	Categorized int
	AssumedOut  int

	Suspects []suspectRow
	Preview  []previewRow
}

const previewLimit = 12

// ImportCSV reads path and appends what it finds to db. The whole file is parsed
// and validated before anything is appended, so a row budgit cannot understand
// aborts the import with nothing written — a half-imported statement is worse
// than none. The caller saves.
func ImportCSV(db *DB, path string, acctID int, opts importOptions) (*importResult, error) {
	if opts.Invert && opts.NoInvert {
		return nil, fmt.Errorf("--invert and --no-invert contradict each other")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	d, rows, rowLines, err := detectDialect(data, opts)
	if err != nil {
		return nil, err
	}
	if opts.Invert || opts.NoInvert {
		if d.Mode != modeSigned {
			return nil, fmt.Errorf("--invert only applies to a single signed amount column; this file uses %s", d.Mode)
		}
	} else if err := d.checkSigns(rows); err != nil {
		return nil, err
	}
	d.Invert = opts.Invert

	resolver, err := newCategoryResolver(db, opts)
	if err != nil {
		return nil, err
	}

	seen := db.ExternalIDs()

	// claimable indexes what you have already entered by hand on this account,
	// so a statement row can recognise a purchase you typed yourself instead of
	// recording it twice. Only rows with no external id are claimable: anything
	// carrying one is already a known bank record.
	claimable := map[claimKey][]int{}
	// settled holds the opposite: rows that already carry a bank id. They are
	// never claimed, but a new row landing on top of one is worth saying out
	// loud — some banks renumber their own ids between exports, which makes an
	// overlapping download look like fresh transactions.
	settled := map[claimKey][]int{}
	for i, t := range db.Transactions {
		if t.AccountID != acctID {
			continue
		}
		k := claimKey{t.Date, t.AmountCents}
		if t.ExternalID == "" {
			claimable[k] = append(claimable[k], i)
		} else {
			settled[k] = append(settled[k], i)
		}
	}

	res := &importResult{Path: path, Dialect: d, Rows: len(rows)}

	// Staged, not appended: nothing touches db until the whole file parses.
	type staged struct {
		date, desc, extID string
		catID             int
		cents             int64
	}
	var pending []staged

	// Adoptions are staged like everything else, so a row budgit cannot read
	// still leaves the file untouched.
	type adoption struct {
		txn   int
		extID string
	}
	var adoptions []adoption

	for i, row := range rows {
		line := rowLines[i]

		if d.Status != colAbsent {
			if s := normHeader(cell(row, d.Status)); s != "" && s != "posted" {
				res.Pending++
				continue
			}
		}

		date, err := parseCSVDate(cell(row, d.Date), opts.DateFormat)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if date == "" && d.Date2 != colAbsent {
			if date, err = parseCSVDate(cell(row, d.Date2), opts.DateFormat); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
		}

		cents, assumed, err := d.rowAmount(row)
		switch {
		case errors.Is(err, errNoAmount):
			res.Junk++ // blank separators and trailing "Totals" rows
			continue
		case err != nil:
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if date == "" {
			// An amount with nowhere to put it. Pending card holds look like this.
			res.Junk++
			continue
		}
		if cents == 0 {
			res.Zero++
			continue
		}

		extID := cell(row, d.ExtID)
		if extID != "" {
			if seen[extID] {
				res.Duplicates++
				continue
			}
			seen[extID] = true // also catches a file that repeats itself
		}

		// Pair off against a hand-entered row for the same day and the same
		// exact cents. One statement row claims at most one typed row, so two
		// genuine identical purchases still end up as two transactions.
		if idxs := claimable[claimKey{date, cents}]; len(idxs) > 0 {
			adoptions = append(adoptions, adoption{idxs[0], extID})
			claimable[claimKey{date, cents}] = idxs[1:]
			res.Matched++
			continue
		}

		catID := resolver.resolve(cell(row, d.Category))
		if catID != 0 {
			res.Categorized++
		}
		if assumed {
			res.AssumedOut++
		}

		desc := cell(row, d.Desc)
		// Consumed one-for-one, so a file holding three genuine repeats against
		// one existing row warns once rather than three times.
		if idxs := settled[claimKey{date, cents}]; len(idxs) > 0 {
			res.Suspects = append(res.Suspects, suspectRow{
				Date: date, Desc: desc, Cents: cents,
				ExistingID: db.Transactions[idxs[0]].ID,
			})
			settled[claimKey{date, cents}] = idxs[1:]
		}
		pending = append(pending, staged{date, desc, extID, catID, cents})
		if len(res.Preview) < previewLimit {
			res.Preview = append(res.Preview, previewRow{
				Date: date, Desc: desc, Cents: cents,
				Category: db.CategoryName(catID),
			})
		}
	}

	res.Imported = len(pending)
	if opts.DryRun {
		return res, nil
	}
	// Attaching the bank's id to what you typed is what makes the match stick:
	// the next import recognises the row outright. Your description and
	// category are left exactly as you wrote them.
	for _, a := range adoptions {
		db.Transactions[a.txn].ExternalID = a.extID
	}
	for _, p := range pending {
		appendTransaction(db, p.date, acctID, p.catID, p.desc, p.cents, p.extID)
	}
	return res, nil
}

// claimKey is how a statement row recognises a transaction you typed yourself:
// same day, same exact cents. Descriptions are deliberately not compared —
// banks rewrite them ("Amazon" arrives as "AMAZON RETA* 5R26W6..."), so
// requiring them to look alike would miss almost every real match.
type claimKey struct {
	date  string
	cents int64
}
