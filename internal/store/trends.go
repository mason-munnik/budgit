package store

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Trends buckets money by calendar period and compares it with the buckets
// before (or a year before). Weeks start Monday. Semantics match the report:
// spent is categorized expense net of refunds, uncategorized is outflow only,
// and net is Report.NetCents.

// Finest first, with default bucket counts.
var trendPeriods = []struct {
	name     string
	defCount int
}{
	{"day", 30}, {"week", 12}, {"month", 12}, {"quarter", 8}, {"half", 6}, {"year", 5},
}

const maxTrendBuckets = 120

func periodRank(p string) int {
	for i, tp := range trendPeriods {
		if tp.name == p {
			return i
		}
	}
	return -1
}

// Within replaces Count: every Period bucket inside the Within bucket holding End.
type TrendQuery struct {
	Period  string
	Count   int
	Within  string
	End     string
	Compare string // previous (default) | year | none
}

// Start/End are inclusive. Partial means clipped by the range edge.
type TrendBucket struct {
	Label   string `json:"label"`
	Title   string `json:"title"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Partial bool   `json:"partial"`
}

// Values and Compare are cents, one per bucket.
type TrendSeries struct {
	Key          string  `json:"key"` // spent, income, net, uncategorized, cat:<id>
	Name         string  `json:"name"`
	Kind         string  `json:"kind"` // expense | income | net | uncategorized
	Summary      bool    `json:"summary"`
	Values       []int64 `json:"values"`
	Compare      []int64 `json:"compare"`
	Total        int64   `json:"total"`
	CompareTotal int64   `json:"compare_total"`
}

type TrendsResult struct {
	Period         string        `json:"period"`
	Count          int           `json:"count"`
	Within         string        `json:"within,omitempty"`
	Compare        string        `json:"compare"`
	Start          string        `json:"start"`
	End            string        `json:"end"`
	Label          string        `json:"label"`
	CompareLabel   string        `json:"compare_label"`
	Latest         string        `json:"latest"`
	PrevEnd        string        `json:"prev_end"`
	NextEnd        string        `json:"next_end"`
	DataStart      string        `json:"data_start"`
	DataEnd        string        `json:"data_end"`
	Buckets        []TrendBucket `json:"buckets"`
	CompareBuckets []TrendBucket `json:"compare_buckets"`
	Series         []TrendSeries `json:"series"`
}

const isoDate = "2006-01-02"

func day(s string) time.Time {
	t, _ := time.Parse(isoDate, s)
	return t
}

func iso(t time.Time) string { return t.Format(isoDate) }

// floorTo returns the bucket start; always day 1 for months, so AddDate is safe.
func floorTo(p string, t time.Time) time.Time {
	y, m, _ := t.Date()
	switch p {
	case "week":
		return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	case "quarter":
		return time.Date(y, (m-1)/3*3+1, 1, 0, 0, 0, 0, time.UTC)
	case "half":
		return time.Date(y, (m-1)/6*6+1, 1, 0, 0, 0, 0, time.UTC)
	case "year":
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return t
}

func step(p string, t time.Time, n int) time.Time {
	switch p {
	case "week":
		return t.AddDate(0, 0, 7*n)
	case "month":
		return t.AddDate(0, n, 0)
	case "quarter":
		return t.AddDate(0, 3*n, 0)
	case "half":
		return t.AddDate(0, 6*n, 0)
	case "year":
		return t.AddDate(n, 0, 0)
	}
	return t.AddDate(0, 0, n)
}

func lastDay(p string, start time.Time) time.Time { return step(p, start, 1).AddDate(0, 0, -1) }

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func days(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

// 364 days keeps weeks Monday-aligned.
func yearBack(p string, t time.Time) time.Time {
	if p == "day" || p == "week" {
		return t.AddDate(0, 0, -364)
	}
	return t.AddDate(-1, 0, 0)
}

// LatestTrendEnd is the newest transaction date, capped at today.
func LatestTrendEnd(db *DB, today string) string {
	latest := ""
	for _, t := range db.Transactions {
		if t.Date > latest {
			latest = t.Date
		}
	}
	if latest == "" || latest > today {
		return today
	}
	return latest
}

func BuildTrends(db *DB, q TrendQuery, today string) (TrendsResult, error) {
	var res TrendsResult
	rank := periodRank(q.Period)
	if rank < 0 {
		return res, fmt.Errorf("period %q must be day, week, month, quarter, half or year", q.Period)
	}
	compare := q.Compare
	if compare == "" {
		compare = "previous"
	}
	if compare != "previous" && compare != "year" && compare != "none" {
		return res, fmt.Errorf("compare %q must be previous, year or none", q.Compare)
	}

	latest := LatestTrendEnd(db, today)
	endS := latest
	if q.End != "" {
		v, err := ValidateDate(q.End)
		if err != nil {
			return res, err
		}
		endS = v
	}
	end := day(endS)
	if end.Year() < 1900 || end.Year() > 9000 {
		return res, fmt.Errorf("end %q is out of range", q.End)
	}

	var first, last, rangeStart time.Time
	count := q.Count
	if q.Within != "" {
		wr := periodRank(q.Within)
		if wr < 0 {
			return res, fmt.Errorf("within %q must be day, week, month, quarter, half or year", q.Within)
		}
		if wr <= rank {
			return res, fmt.Errorf("within %q must be a longer period than %q", q.Within, q.Period)
		}
		if q.Count != 0 {
			return res, fmt.Errorf("give count or within, not both")
		}
		rangeStart = floorTo(q.Within, end)
		first, last = floorTo(q.Period, rangeStart), floorTo(q.Period, end)
		count = 0
		for t := first; !t.After(last); t = step(q.Period, t, 1) {
			count++
		}
	} else {
		if count == 0 {
			count = trendPeriods[rank].defCount
		}
		if count < 1 || count > maxTrendBuckets {
			return res, fmt.Errorf("count %d must be 1 to %d", count, maxTrendBuckets)
		}
		last = floorTo(q.Period, end)
		first = step(q.Period, last, -(count - 1))
		rangeStart = first
	}
	if count > maxTrendBuckets {
		return res, fmt.Errorf("that is %d buckets; the most is %d", count, maxTrendBuckets)
	}

	res = TrendsResult{
		Period: q.Period, Count: count, Within: q.Within, Compare: compare,
		Start: iso(rangeStart), End: endS, Latest: latest,
		Buckets: make([]TrendBucket, count), CompareBuckets: []TrendBucket{},
	}
	if compare != "none" {
		res.CompareBuckets = make([]TrendBucket, count)
	}
	for i := 0; i < count; i++ {
		fs := step(q.Period, first, i)
		fe := lastDay(q.Period, fs)
		s, e := maxT(fs, rangeStart), minT(fe, end)
		res.Buckets[i] = makeBucket(q.Period, fs, s, e, i == 0 || fs.Year() != step(q.Period, fs, -1).Year())
		if compare == "none" {
			continue
		}
		if q.Within != "" {
			res.CompareBuckets[i] = withinCompare(q, compare, rangeStart, end, fs, s, e, i == count-1)
			continue
		}
		cfs := step(q.Period, fs, -count)
		if compare == "year" {
			cfs = yearBack(q.Period, fs)
		}
		// Clip only edges the range clipped, so a whole Feb still compares with a whole Jan.
		cfe := lastDay(q.Period, cfs)
		cs, ce := cfs, cfe
		if s != fs {
			cs = cfs.AddDate(0, 0, days(fs, s))
		}
		if e != fe {
			ce = minT(cfs.AddDate(0, 0, days(fs, e)), cfe)
		}
		res.CompareBuckets[i] = makeBucket(q.Period, cfs, cs, ce, i == 0 || cfs.Year() != step(q.Period, cfs, -1).Year())
	}

	res.Label = spanLabel(rangeStart, end)
	if len(res.CompareBuckets) > 0 {
		res.CompareLabel = spanLabel(day(res.CompareBuckets[0].Start), day(res.CompareBuckets[count-1].End))
	}

	aggregate(db, &res)

	for _, t := range db.Transactions {
		if res.DataStart == "" || t.Date < res.DataStart {
			res.DataStart = t.Date
		}
		if t.Date > res.DataEnd {
			res.DataEnd = t.Date
		}
	}
	prev := rangeStart.AddDate(0, 0, -1)
	if res.DataStart != "" && res.DataStart <= iso(prev) {
		res.PrevEnd = iso(prev)
	}
	if endS < latest {
		next := lastDay(q.Period, step(q.Period, last, count))
		if q.Within != "" {
			next = lastDay(q.Within, step(q.Within, rangeStart, 1))
		}
		res.NextEnd = iso(minT(next, day(latest)))
	}
	return res, nil
}

// withinCompare maps a drilled-in bucket onto the same days of the previous
// (or last year's) parent period, so the buckets sum to that period's total.
func withinCompare(q TrendQuery, compare string, parent, end, fs, s, e time.Time, isLast bool) TrendBucket {
	cp := step(q.Within, parent, -1)
	if compare == "year" {
		cp = yearBack(q.Within, parent)
	}
	cpEnd := lastDay(q.Within, cp)
	var cs, ce time.Time
	if periodRank(q.Period) >= periodRank("month") {
		mo := (cp.Year()-parent.Year())*12 + int(cp.Month()) - int(parent.Month())
		cs = s.AddDate(0, mo, 0)
		ce = lastDay(q.Period, cs)
		if e != lastDay(q.Period, fs) {
			ce = minT(ce, cs.AddDate(0, 0, days(s, e)))
		}
	} else {
		cs = cp.AddDate(0, 0, days(parent, s))
		ce = minT(cp.AddDate(0, 0, days(parent, e)), cpEnd)
		if isLast && end.Equal(lastDay(q.Within, parent)) {
			ce = cpEnd // a short month's last bucket takes a longer month's tail
		}
	}
	if cs.After(cpEnd) {
		return TrendBucket{Start: iso(cpEnd.AddDate(0, 0, 1)), End: iso(cpEnd), Partial: true, Title: "no matching days"}
	}
	b := makeBucket(q.Period, floorTo(q.Period, cs), cs, ce, false)
	if periodRank(q.Period) < periodRank("month") {
		b.Title = spanLabel(cs, ce)
	}
	return b
}

func makeBucket(p string, fs, s, e time.Time, withYear bool) TrendBucket {
	b := TrendBucket{Start: iso(s), End: iso(e), Partial: s != fs || e != lastDay(p, fs)}
	switch p {
	case "day":
		b.Label, b.Title = fs.Format("Jan 2"), fs.Format("Mon, Jan 2, 2006")
	case "week":
		b.Label, b.Title = fs.Format("Jan 2"), "Week of "+fs.Format("Jan 2, 2006")
	case "month":
		b.Label, b.Title = fs.Format("Jan 2006"), fs.Format("January 2006")
	case "quarter":
		b.Label = "Q" + strconv.Itoa((int(fs.Month())-1)/3+1) + " " + strconv.Itoa(fs.Year())
		b.Title = b.Label
	case "half":
		b.Label = fs.Format("Jan") + "–" + lastDay(p, fs).Format("Jan 2006")
		b.Title = b.Label
	case "year":
		b.Label, b.Title = strconv.Itoa(fs.Year()), strconv.Itoa(fs.Year())
	}
	if withYear && (p == "day" || p == "week") {
		b.Label += " '" + fs.Format("06")
	}
	if p != "day" && (b.Partial || p == "week") {
		b.Title += " · " + spanLabel(s, e)
	}
	return b
}

func spanLabel(a, b time.Time) string {
	switch {
	case a.Equal(b):
		return a.Format("Jan 2, 2006")
	case a.Year() != b.Year():
		return a.Format("Jan 2, 2006") + " – " + b.Format("Jan 2, 2006")
	case a.Month() != b.Month():
		return a.Format("Jan 2") + " – " + b.Format("Jan 2, 2006")
	}
	return a.Format("Jan 2") + " – " + b.Format("2, 2006")
}

// aggregate fills res.Series; ISO date strings sort, so buckets are binary-searched.
func aggregate(db *DB, res *TrendsResult) {
	n := len(res.Buckets)
	type acc struct {
		spent, income, uncatOut, uncatSigned []int64
		cats                                 map[int][]int64
	}
	newAcc := func() *acc {
		return &acc{make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n), map[int][]int64{}}
	}
	cur, prev := newAcc(), newAcc()

	find := func(bs []TrendBucket, date string) int {
		i := sort.Search(len(bs), func(i int) bool { return bs[i].Start > date }) - 1
		if i >= 0 && date <= bs[i].End {
			return i
		}
		return -1
	}
	add := func(a *acc, i int, t Transaction) {
		if t.CategoryID == 0 {
			a.uncatSigned[i] += t.AmountCents
			if t.AmountCents < 0 {
				a.uncatOut[i] -= t.AmountCents
			}
			return
		}
		c := db.CategoryByID(t.CategoryID)
		if c == nil {
			return // dangling id; the report skips these too
		}
		v := t.AmountCents
		if c.Kind == KindExpense {
			v = -v
			a.spent[i] += v
		} else {
			a.income[i] += v
		}
		if a.cats[c.ID] == nil {
			a.cats[c.ID] = make([]int64, n)
		}
		a.cats[c.ID][i] += v
	}
	for _, t := range db.Transactions {
		if i := find(res.Buckets, t.Date); i >= 0 {
			add(cur, i, t)
		}
		if i := find(res.CompareBuckets, t.Date); i >= 0 {
			add(prev, i, t)
		}
	}

	hasCmp := len(res.CompareBuckets) > 0
	series := func(key, name, kind string, summary bool, v, c []int64) TrendSeries {
		s := TrendSeries{Key: key, Name: name, Kind: kind, Summary: summary, Values: v, Compare: []int64{}}
		if hasCmp {
			s.Compare = c
		}
		for _, x := range s.Values {
			s.Total += x
		}
		for _, x := range s.Compare {
			s.CompareTotal += x
		}
		return s
	}
	net := func(a *acc) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = a.income[i] - a.spent[i] + a.uncatSigned[i]
		}
		return out
	}
	res.Series = []TrendSeries{
		series("spent", "Total spent", "expense", true, cur.spent, prev.spent),
		series("income", "Total income", "income", true, cur.income, prev.income),
		series("net", "Net", "net", true, net(cur), net(prev)),
		series("uncategorized", "Uncategorized", "uncategorized", true, cur.uncatOut, prev.uncatOut),
	}

	// Categories with activity on either side: expenses first, biggest first.
	var cats []TrendSeries
	for _, c := range db.Categories {
		v, c1 := cur.cats[c.ID]
		p, c2 := prev.cats[c.ID]
		if !c1 && !c2 {
			continue
		}
		if v == nil {
			v = make([]int64, n)
		}
		if p == nil {
			p = make([]int64, n)
		}
		cats = append(cats, series("cat:"+strconv.Itoa(c.ID), c.Name, c.Kind, false, v, p))
	}
	sort.SliceStable(cats, func(i, j int) bool {
		if cats[i].Kind != cats[j].Kind {
			return cats[i].Kind == KindExpense
		}
		return cats[i].Total > cats[j].Total
	})
	res.Series = append(res.Series, cats...)
}
