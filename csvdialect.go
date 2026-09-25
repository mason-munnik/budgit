package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Every bank writes CSV differently, so a file has to be understood before a
// single amount can be read from it. Everything worked out about one file lands
// in a dialect, which --dry-run prints so a misdetection is visible before it
// costs anything.

type amountMode int

const (
	modeSigned amountMode = iota // one Amount column carrying its own sign
	modePair                     // separate Debit and Credit columns
	modeTyped                    // one unsigned Amount plus a direction column
)

func (m amountMode) String() string {
	switch m {
	case modePair:
		return "debit/credit columns"
	case modeTyped:
		return "unsigned amounts with a type column"
	default:
		return "signed amounts"
	}
}

// colAbsent marks a column this file does not have.
const colAbsent = -1

type dialect struct {
	Delimiter  rune
	DecimalSep byte // '.' or ',' — ';' files normally pair with a decimal comma
	HeaderRow  int  // index of the real header record; preamble sits above it
	HeaderLine int  // that header's actual line in the file
	Header     []string
	Mode       amountMode

	Date, Date2                         int
	Amount, Debit, Credit               int
	Desc, Category, ExtID, Status, Type int

	Invert bool
}

// columnAliases lists the header names each field is known by, best first, so
// "Posting Date" wins over "Effective Date" when a file carries both.
var columnAliases = map[string][]string{
	"date":     {"posting date", "transaction date", "posted date", "date", "effective date"},
	"amount":   {"amount", "transaction amount", "value"},
	"debit":    {"debit", "debit amount", "withdrawal", "withdrawals", "money out"},
	"credit":   {"credit", "credit amount", "deposit", "deposits", "money in"},
	"desc":     {"description", "payee", "merchant", "name", "memo", "details"},
	"category": {"transaction category", "category"},
	"extid":    {"transaction id", "reference number", "reference", "id"},
	"status":   {"posting status", "status"},
	"type":     {"transaction type", "type", "debit/credit"},
}

// normHeader folds a header cell to the form the alias tables are written in.
func normHeader(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.Trim(s, `"'`)
	return strings.Join(strings.Fields(s), " ")
}

// findCol returns the first column matching any alias, skipping indices already
// claimed by another field. Matching is exact: substring matching over a whole
// header row produces surprises that only show up as wrong money.
func findCol(header []string, aliases []string, taken ...int) int {
	for _, want := range aliases {
		for i, h := range header {
			if normHeader(h) != want || containsInt(taken, i) {
				continue
			}
			return i
		}
	}
	return colAbsent
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// stripBOM removes the byte order mark Windows exports start with. Without it
// the first column reads as "\ufeffDate", matches no alias, and the failure
// looks like an unknown bank rather than an encoding detail.
func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// sniffDelimiter picks whichever separator yields the most consistent table.
func sniffDelimiter(data []byte) rune {
	best, bestScore := ',', -1
	for _, d := range []rune{',', ';', '\t', '|'} {
		if s := delimiterScore(data, d); s > bestScore {
			best, bestScore = d, s
		}
	}
	return best
}

func delimiterScore(data []byte, d rune) int {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = d
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	counts := map[int]int{}
	for i := 0; i < 20; i++ {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) > 1 {
			counts[len(rec)]++
		}
	}
	// Reward width and agreement together: a file split on the wrong character
	// tends to give one wide row and a pile of narrow ones.
	score := -1
	for width, rows := range counts {
		if s := width * rows; s > score {
			score = s
		}
	}
	return score
}

// readRecords parses the whole file with one delimiter, tolerating ragged rows
// and the stray unescaped quote that bank exports contain. It also returns each
// record's real line in the file: the reader silently drops blank lines, so a
// record index would point at the wrong row in every error message.
func readRecords(data []byte, comma rune) ([][]string, []int, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	var recs [][]string
	var lines []int
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("cannot read the file as CSV: %w", err)
		}
		line := 0
		if len(rec) > 0 {
			line, _ = r.FieldPos(0)
		}
		recs = append(recs, rec)
		lines = append(lines, line)
	}
	return recs, lines, nil
}

// headerScanLimit is how far down the file to look for the real header. Chase
// and Wells Fargo put account details above it.
const headerScanLimit = 25

// resolveColumns maps one candidate header row onto field positions.
func resolveColumns(header []string, over map[string]string) (*dialect, error) {
	d := &dialect{
		Header: header,
		Date:   colAbsent, Date2: colAbsent, Amount: colAbsent,
		Debit: colAbsent, Credit: colAbsent, Desc: colAbsent,
		Category: colAbsent, ExtID: colAbsent, Status: colAbsent, Type: colAbsent,
	}

	pick := func(field string, target *int, taken ...int) error {
		if raw, ok := over[field]; ok && strings.TrimSpace(raw) != "" {
			i, err := resolveOverride(header, raw)
			if err != nil {
				return fmt.Errorf("--%s-col: %w", field, err)
			}
			*target = i
			return nil
		}
		*target = findCol(header, columnAliases[field], taken...)
		return nil
	}

	for _, f := range []struct {
		name string
		to   *int
	}{
		{"date", &d.Date}, {"amount", &d.Amount}, {"debit", &d.Debit},
		{"credit", &d.Credit}, {"desc", &d.Desc}, {"category", &d.Category},
		{"extid", &d.ExtID}, {"status", &d.Status}, {"type", &d.Type},
	} {
		if err := pick(f.name, f.to); err != nil {
			return nil, err
		}
	}
	// The secondary date is whatever date-ish column the primary did not take,
	// so a blank "Posting Date" can fall back to "Effective Date".
	d.Date2 = findCol(header, columnAliases["date"], d.Date)
	return d, nil
}

// resolveOverride accepts a header name or a 0-based column index.
func resolveOverride(header []string, raw string) (int, error) {
	want := normHeader(raw)
	for i, h := range header {
		if normHeader(h) == want {
			return i, nil
		}
	}
	if i, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		if i < 0 || i >= len(header) {
			return 0, fmt.Errorf("column %d is outside this file's %d columns", i, len(header))
		}
		return i, nil
	}
	return 0, fmt.Errorf("no column named %q (this file has: %s)", raw, strings.Join(header, ", "))
}

// usable reports whether a candidate header carries enough to read money from.
func (d *dialect) usable() bool {
	if d.Date == colAbsent {
		return false
	}
	return d.Amount != colAbsent || (d.Debit != colAbsent && d.Credit != colAbsent)
}

// detectDialect finds the header row and works out how amounts are written.
// Overrides in opts win at every step.
func detectDialect(data []byte, opts importOptions) (*dialect, [][]string, []int, error) {
	data = stripBOM(data)

	comma := sniffDelimiter(data)
	if s := strings.TrimSpace(opts.Delimiter); s != "" {
		r, err := parseDelimiter(s)
		if err != nil {
			return nil, nil, nil, err
		}
		comma = r
	}

	records, lines, err := readRecords(data, comma)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil, fmt.Errorf("the file is empty")
	}

	limit := headerScanLimit
	if len(records) < limit {
		limit = len(records)
	}
	var d *dialect
	for i := 0; i < limit; i++ {
		if len(records[i]) < 2 {
			continue // preamble lines are usually one cell wide
		}
		cand, err := resolveColumns(records[i], opts.Cols)
		if err != nil {
			return nil, nil, nil, err
		}
		if cand.usable() {
			cand.HeaderRow = i
			cand.HeaderLine = lines[i]
			d = cand
			break
		}
	}
	if d == nil {
		return nil, nil, nil, fmt.Errorf(
			"could not find a header row in the first %d lines.\n"+
				"       budgit needs a date column and either an amount column or a debit/credit pair.\n"+
				"       Name them yourself with --date-col and --amount-col (or --debit-col/--credit-col)", limit)
	}

	d.Delimiter = comma
	d.DecimalSep = '.'
	// A semicolon file is almost always European, where the comma is the decimal
	// point. Getting this wrong is not cosmetic: ParseMoney strips commas as
	// thousands separators, so "37,44" would import as $3,744.00.
	if comma == ';' {
		d.DecimalSep = ','
	}
	switch strings.TrimSpace(strings.ToLower(opts.Decimal)) {
	case "comma":
		d.DecimalSep = ','
	case "dot", "point", "period":
		d.DecimalSep = '.'
	case "":
	default:
		return nil, nil, nil, fmt.Errorf("--decimal must be dot or comma, got %q", opts.Decimal)
	}

	rows := records[d.HeaderRow+1:]
	rowLines := lines[d.HeaderRow+1:]
	if err := d.chooseMode(rows); err != nil {
		return nil, nil, nil, err
	}
	return d, rows, rowLines, nil
}

func parseDelimiter(s string) (rune, error) {
	switch strings.ToLower(s) {
	case "tab", "\\t":
		return '\t', nil
	case "comma":
		return ',', nil
	case "semicolon":
		return ';', nil
	case "pipe":
		return '|', nil
	}
	r := []rune(s)
	if len(r) != 1 {
		return 0, fmt.Errorf("--delimiter must be a single character (or tab/comma/semicolon/pipe), got %q", s)
	}
	return r[0], nil
}

// chooseMode decides how direction is expressed in this file.
func (d *dialect) chooseMode(rows [][]string) error {
	switch {
	case d.Debit != colAbsent && d.Credit != colAbsent:
		d.Mode = modePair
		return nil
	case d.Amount == colAbsent:
		return fmt.Errorf("this file has no amount column and no debit/credit pair")
	}

	// A file whose amounts are all non-negative cannot be carrying direction in
	// the sign, so it must be carrying it in the type column.
	if d.Type != colAbsent && !d.anyNegative(rows) && d.typeColumnSpeaksDirection(rows) {
		d.Mode = modeTyped
		return nil
	}
	d.Mode = modeSigned
	return nil
}

func (d *dialect) anyNegative(rows [][]string) bool {
	for _, r := range rows {
		cents, ok := d.rawAmount(r)
		if ok && cents < 0 {
			return true
		}
	}
	return false
}

func (d *dialect) typeColumnSpeaksDirection(rows [][]string) bool {
	for _, r := range rows {
		if directionWord(cell(r, d.Type)) != 0 {
			return true
		}
	}
	return false
}

// rawAmount parses the amount column exactly as written, for detection only.
func (d *dialect) rawAmount(row []string) (int64, bool) {
	norm, err := normalizeAmount(cell(row, d.Amount), d.DecimalSep)
	if err != nil || norm == "" {
		return 0, false
	}
	cents, _, err := ParseMoney(norm)
	if err != nil {
		return 0, false
	}
	return cents, true
}

func cell(row []string, i int) string {
	if i == colAbsent || i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// Direction words are kept conservative on purpose. "payment" is deliberately
// absent: a card payment is a credit to the card and a debit to the account it
// came from, so it carries no direction on its own.
var (
	debitWords  = map[string]bool{"debit": true, "withdrawal": true, "sale": true, "purchase": true, "dr": true}
	creditWords = map[string]bool{"credit": true, "deposit": true, "refund": true, "return": true, "cr": true}
)

// directionWord returns -1 for money leaving, +1 for money arriving, 0 for no
// opinion.
func directionWord(s string) int {
	v := normHeader(s)
	switch {
	case debitWords[v]:
		return -1
	case creditWords[v]:
		return 1
	}
	return 0
}

// signConflictError is raised when a file's amount signs contradict its own type
// column. budgit refuses to guess here: silently reversing every row in a
// statement is the worst thing this importer could do.
type signConflictError struct {
	debitPositive, creditNegative int
	debitLabel, creditLabel       string
	agree                         int
}

func (e *signConflictError) Error() string {
	var b strings.Builder
	b.WriteString("this file's signs look inverted.\n")
	if e.debitPositive > 0 {
		fmt.Fprintf(&b, "       %d %s typed %q %s POSITIVE amounts\n",
			e.debitPositive, plural(e.debitPositive, "row", "rows"),
			e.debitLabel, plural(e.debitPositive, "carries", "carry"))
	}
	if e.creditNegative > 0 {
		fmt.Fprintf(&b, "       %d %s typed %q %s NEGATIVE amounts\n",
			e.creditNegative, plural(e.creditNegative, "row", "rows"),
			e.creditLabel, plural(e.creditNegative, "carries", "carry"))
	}
	b.WriteString("\n       A checking-account export is normally the opposite.\n")
	b.WriteString("       If purchases here are positive, re-run with   --invert\n")
	b.WriteString("       If the signs are already correct, confirm with --no-invert\n")
	b.WriteString("\n       Nothing was imported.")
	return b.String()
}

// checkSigns compares each row's sign against its type column. It only has an
// opinion in signed mode, and only when the file has a type column at all.
func (d *dialect) checkSigns(rows [][]string) error {
	if d.Mode != modeSigned || d.Type == colAbsent {
		return nil
	}
	e := &signConflictError{}
	labels := map[string]int{}
	for _, r := range rows {
		dir := directionWord(cell(r, d.Type))
		if dir == 0 {
			continue
		}
		cents, ok := d.rawAmount(r)
		if !ok || cents == 0 {
			continue
		}
		label := cell(r, d.Type)
		switch {
		case dir < 0 && cents > 0:
			e.debitPositive++
			labels["-"+label]++
		case dir > 0 && cents < 0:
			e.creditNegative++
			labels["+"+label]++
		default:
			e.agree++
		}
	}
	if e.debitPositive+e.creditNegative <= e.agree {
		return nil
	}
	e.debitLabel = commonestLabel(labels, "-")
	e.creditLabel = commonestLabel(labels, "+")
	return e
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func commonestLabel(labels map[string]int, prefix string) string {
	type kv struct {
		k string
		n int
	}
	var all []kv
	for k, n := range labels {
		if strings.HasPrefix(k, prefix) {
			all = append(all, kv{strings.TrimPrefix(k, prefix), n})
		}
	}
	if len(all) == 0 {
		return ""
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	return all[0].k
}

// Summary is the one-line description --dry-run prints so the user can see what
// was inferred before trusting it.
func (d *dialect) Summary() string {
	name := func(i int) string {
		if i == colAbsent || i >= len(d.Header) {
			return "—"
		}
		return strings.TrimSpace(d.Header[i])
	}
	delim := map[rune]string{',': "comma", ';': "semicolon", '\t': "tab", '|': "pipe"}[d.Delimiter]
	if delim == "" {
		delim = strconv.QuoteRune(d.Delimiter)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Detected: %s-delimited, header on line %d, %s", delim, d.HeaderLine, d.Mode)
	if d.DecimalSep == ',' {
		b.WriteString(", decimal comma")
	}
	if d.Invert {
		b.WriteString(", signs inverted")
	}
	b.WriteString("\n  date=" + name(d.Date))
	if d.Date2 != colAbsent {
		b.WriteString(" fallback=" + name(d.Date2))
	}
	if d.Mode == modePair {
		b.WriteString("  debit=" + name(d.Debit) + "  credit=" + name(d.Credit))
	} else {
		b.WriteString("  amount=" + name(d.Amount))
	}
	b.WriteString("  desc=" + name(d.Desc))
	if d.Category != colAbsent {
		b.WriteString("\n  category=" + name(d.Category))
	}
	if d.ExtID != colAbsent {
		b.WriteString("  id=" + name(d.ExtID))
	}
	if d.Type != colAbsent {
		b.WriteString("  type=" + name(d.Type))
	}
	if d.Status != colAbsent {
		b.WriteString("  status=" + name(d.Status))
	}
	return b.String()
}
