package store

import (
	"strings"
	"testing"
)

func trendDB() *DB {
	db := &DB{
		Categories: []Category{{1, "Groceries", KindExpense}, {2, "Salary", KindIncome}, {3, "Dining", KindExpense}},
	}
	add := func(date string, cat int, cents int64) {
		db.Transactions = append(db.Transactions, Transaction{ID: len(db.Transactions) + 1, Date: date, CategoryID: cat, AmountCents: cents})
	}
	add("2025-12-30", 1, -1000) // Tuesday of the week of Mon Dec 29
	add("2026-01-02", 1, -2000)
	add("2026-01-15", 2, 500000)
	add("2026-02-10", 3, -3000)
	add("2026-02-11", 3, 5000) // refund bigger than the month's spend
	add("2026-03-05", 0, -700)
	add("2026-03-06", 0, 900)
	add("2026-03-20", 1, -4000)
	return db
}

func mustTrends(t *testing.T, db *DB, q TrendQuery) TrendsResult {
	t.Helper()
	res, err := BuildTrends(db, q, "2026-03-31")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func seriesByKey(res TrendsResult, key string) TrendSeries {
	for _, s := range res.Series {
		if s.Key == key {
			return s
		}
	}
	return TrendSeries{}
}

func TestTrendsMonthly(t *testing.T) {
	res := mustTrends(t, trendDB(), TrendQuery{Period: "month", Count: 3})
	if res.End != "2026-03-20" {
		t.Errorf("end = %s, want the latest transaction", res.End)
	}
	var starts []string
	for _, b := range res.Buckets {
		starts = append(starts, b.Start)
	}
	if strings.Join(starts, ",") != "2026-01-01,2026-02-01,2026-03-01" {
		t.Errorf("buckets %v", starts)
	}
	if !res.Buckets[2].Partial || res.Buckets[2].End != "2026-03-20" {
		t.Errorf("March should be partial through the 20th: %+v", res.Buckets[2])
	}
	// The partial March compares with Dec 1-20, and Dec 30 falls outside it.
	if c := res.CompareBuckets[2]; c.Start != "2025-12-01" || c.End != "2025-12-20" {
		t.Errorf("compare bucket %+v", c)
	}
	spent := seriesByKey(res, "spent")
	want := []int64{2000, -2000, 4000}
	for i := range want {
		if spent.Values[i] != want[i] {
			t.Errorf("spent[%d] = %d, want %d", i, spent.Values[i], want[i])
		}
	}
	if spent.CompareTotal != 0 {
		t.Errorf("compare total %d, want 0", spent.CompareTotal)
	}
	if u := seriesByKey(res, "uncategorized"); u.Values[2] != 700 {
		t.Errorf("uncategorized counts outflow only: %v", u.Values)
	}
	if n := seriesByKey(res, "net"); n.Values[2] != -4000+200 {
		t.Errorf("net March = %d", n.Values[2])
	}
	if res.Series[4].Key != "cat:1" || res.Series[len(res.Series)-1].Key != "cat:2" {
		t.Errorf("category order: %v", res.Series)
	}
}

func TestTrendsWeekAcrossYear(t *testing.T) {
	res := mustTrends(t, trendDB(), TrendQuery{Period: "week", Count: 2, End: "2026-01-04", Compare: "year"})
	b := res.Buckets[1]
	if b.Start != "2025-12-29" || b.End != "2026-01-04" || b.Partial {
		t.Errorf("year-boundary week %+v", b)
	}
	if got := seriesByKey(res, "spent").Values[1]; got != 3000 {
		t.Errorf("week spent %d, want 3000", got)
	}
	if c := res.CompareBuckets[1]; c.Start != "2024-12-30" {
		t.Errorf("year-back week should stay a Monday: %+v", c)
	}
}

func TestTrendsWithinMonthClipsWeeks(t *testing.T) {
	res := mustTrends(t, trendDB(), TrendQuery{Period: "week", Within: "month", End: "2026-01-31"})
	first, last := res.Buckets[0], res.Buckets[len(res.Buckets)-1]
	if first.Start != "2026-01-01" || !first.Partial || last.End != "2026-01-31" {
		t.Errorf("edges %+v .. %+v", first, last)
	}
	if got := seriesByKey(res, "spent").Total; got != 2000 {
		t.Errorf("weeks of January sum to %d, want 2000", got)
	}
	if res.PrevEnd != "2025-12-31" || res.NextEnd != "2026-02-28" {
		t.Errorf("paging %q %q", res.PrevEnd, res.NextEnd)
	}
}

func TestTrendsMonthEndOverflow(t *testing.T) {
	res := mustTrends(t, trendDB(), TrendQuery{Period: "month", Count: 3, End: "2025-01-31"})
	if res.Buckets[0].Start != "2024-11-01" || res.Buckets[2].Start != "2025-01-01" {
		t.Errorf("buckets %+v", res.Buckets)
	}
}

func TestTrendsZeroFillAndEmpty(t *testing.T) {
	res := mustTrends(t, &DB{}, TrendQuery{Period: "quarter", Compare: "none"})
	if len(res.Buckets) != 8 || len(res.CompareBuckets) != 0 {
		t.Fatalf("%d buckets, %d compare", len(res.Buckets), len(res.CompareBuckets))
	}
	for _, s := range res.Series {
		if len(s.Values) != 8 || s.Compare == nil {
			t.Errorf("%s: %v %v", s.Key, s.Values, s.Compare)
		}
	}
}

func TestTrendsBadQuery(t *testing.T) {
	for _, q := range []TrendQuery{
		{Period: "decade"},
		{Period: "month", Count: 500},
		{Period: "month", End: "2026-13-01"},
		{Period: "month", Compare: "sideways"},
		{Period: "month", Within: "week"},
		{Period: "week", Within: "month", Count: 3},
	} {
		if _, err := BuildTrends(trendDB(), q, "2026-03-31"); err == nil {
			t.Errorf("%+v: want an error", q)
		}
	}
}

func TestTrendsWithinCompareMatchesParent(t *testing.T) {
	db := trendDB()
	res := mustTrends(t, db, TrendQuery{Period: "week", Within: "month", End: "2026-03-31"})
	if res.CompareLabel != "Feb 1 – 28, 2026" {
		t.Errorf("compare label %q", res.CompareLabel)
	}
	feb := mustTrends(t, db, TrendQuery{Period: "month", Count: 1, End: "2026-02-28"})
	if got, want := seriesByKey(res, "spent").CompareTotal, seriesByKey(feb, "spent").Total; got != want {
		t.Errorf("weeks of Feb sum to %d, month says %d", got, want)
	}
	if c := res.CompareBuckets[0]; c.Start != "2026-02-01" {
		t.Errorf("first compare bucket %+v", c)
	}

	// June 2025 ends on a one-day Monday week; its compare still runs to May 31.
	jun := mustTrends(t, db, TrendQuery{Period: "week", Within: "month", End: "2025-06-30"})
	if c := jun.CompareBuckets[len(jun.CompareBuckets)-1]; c.Start != "2025-05-30" || c.End != "2025-05-31" {
		t.Errorf("tail bucket %+v", c)
	}
}
