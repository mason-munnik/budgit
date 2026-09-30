package money

import "testing"

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in       string
		cents    int64
		explicit bool
	}{
		{"0", 0, false},
		{"5", 500, false},
		{"84.31", 8431, false},
		{"5.1", 510, false}, // right-padded: 10 cents, not 1
		{"5.01", 501, false},
		{"$1,299.99", 129999, false},
		{"-45.20", -4520, true},
		{"+24.99", 2499, true},
		{"-$1,800", -180000, true},
		{".50", 50, false},
		{"1234567.89", 123456789, false},
		{"12,345,678.90", 1234567890, false},
		{"- 5", -500, true},
		{"999999999999.99", 99999999999999, false}, // the largest allowed
	}
	for _, c := range cases {
		got, explicit, err := ParseMoney(c.in)
		if err != nil {
			t.Errorf("ParseMoney(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.cents {
			t.Errorf("ParseMoney(%q) = %d cents, want %d", c.in, got, c.cents)
		}
		if explicit != c.explicit {
			t.Errorf("ParseMoney(%q) explicitSign = %v, want %v", c.in, explicit, c.explicit)
		}
	}
}

func TestParseMoneyRejects(t *testing.T) {
	for _, in := range []string{
		"", "abc", "1.234", "$", "-", "1.2.3", ".",
		"1.-5", "+-5", "--5", "1.+5", // a second sign hiding past the first
		"1,50", "1,5", "12,34.00", ",123", "1,,000", // a decimal comma, not grouping
		"1000000000000", "99999999999999999", // would overflow or is a typo
	} {
		if _, _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) should have errored", in)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{8431, "$84.31"},
		{-180000, "-$1,800.00"},
		{123456789, "$1,234,567.89"},
		{100, "$1.00"},
		{-5, "-$0.05"},
	}
	for _, c := range cases {
		if got := FormatMoney(c.cents); got != c.want {
			t.Errorf("FormatMoney(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

// Round-tripping must be exact — this is the whole reason for integer cents.
func TestMoneyRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 1, 99, 100, 8431, -4520, 123456789, -1} {
		parsed, _, err := ParseMoney(FormatMoney(cents))
		if err != nil {
			t.Fatalf("round trip of %d failed: %v", cents, err)
		}
		if parsed != cents {
			t.Errorf("round trip: %d -> %q -> %d", cents, FormatMoney(cents), parsed)
		}
	}
}
