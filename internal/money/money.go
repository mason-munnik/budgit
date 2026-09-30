package money

import (
	"fmt"
	"strconv"
	"strings"
)

// Money is always handled as integer cents. Never float.

// ParseMoney parses a human-typed amount into signed cents.
// It reports whether the caller wrote an explicit leading sign, which lets
// the transaction commands infer direction from the category when they didn't.
func ParseMoney(s string) (cents int64, explicitSign bool, err error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, false, fmt.Errorf("empty amount")
	}
	// Commas are checked as thousands grouping below rather than stripped here.
	raw = strings.ReplaceAll(raw, "$", "")
	raw = strings.ReplaceAll(raw, "_", "")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, fmt.Errorf("amount %q has no digits", s)
	}

	neg := false
	switch raw[0] {
	case '-':
		neg, explicitSign, raw = true, true, raw[1:]
	case '+':
		explicitSign, raw = true, raw[1:]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, fmt.Errorf("amount %q has no digits", s)
	}

	whole, frac := raw, ""
	if i := strings.IndexByte(raw, '.'); i >= 0 {
		whole, frac = raw[:i], raw[i+1:]
	}
	if strings.Contains(whole, ",") {
		// "1,50" is a decimal comma, not grouping; never read it as $150.
		if !validGrouping(whole) {
			return 0, false, fmt.Errorf("amount %q: commas only separate thousands here — use a dot for cents, e.g. 1.50", s)
		}
		whole = strings.ReplaceAll(whole, ",", "")
	}
	// Rejects a second sign, as in "+-5" or "1.-5".
	if !allDigits(whole) || !allDigits(frac) || whole+frac == "" {
		return 0, false, fmt.Errorf("invalid amount %q", s)
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 2 {
		return 0, false, fmt.Errorf("amount %q has more than 2 decimal places", s)
	}
	// Right-pad so "5.1" means 10 cents, not 1.
	for len(frac) < 2 {
		frac += "0"
	}
	if len(strings.TrimLeft(whole, "0")) > maxWholeDigits {
		return 0, false, fmt.Errorf("amount %q is too large", s)
	}

	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid amount %q", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid amount %q", s)
	}
	cents = w*100 + f
	if neg {
		cents = -cents
	}
	return cents, explicitSign, nil
}

// maxWholeDigits keeps amounts under $1 trillion, far short of int64 overflow.
const maxWholeDigits = 12

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validGrouping accepts "1,299" and "12,345,678" but not "1,50".
func validGrouping(s string) bool {
	groups := strings.Split(s, ",")
	if len(groups[0]) < 1 || len(groups[0]) > 3 {
		return false
	}
	for _, g := range groups[1:] {
		if len(g) != 3 {
			return false
		}
	}
	return true
}

// FormatMoney renders signed cents as $1,234.56 / -$1,234.56.
func FormatMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%s.%02d", sign, group(cents/100), cents%100)
}

// group inserts thousands separators into a non-negative integer.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
