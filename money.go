package main

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
	// Strip currency symbol and thousands separators.
	raw = strings.ReplaceAll(raw, "$", "")
	raw = strings.ReplaceAll(raw, ",", "")
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
	if raw == "" {
		return 0, false, fmt.Errorf("amount %q has no digits", s)
	}

	whole, frac := raw, ""
	if i := strings.IndexByte(raw, '.'); i >= 0 {
		whole, frac = raw[:i], raw[i+1:]
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
