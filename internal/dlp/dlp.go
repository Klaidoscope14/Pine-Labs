// Package dlp finds and redacts sensitive data before anything leaves the machine
// for a model provider: card numbers (Luhn-checked), Aadhaar numbers
// (Verhoeff-checked), secrets, and email addresses / phone numbers.
// Only counts are ever recorded, never the values.
package dlp

import (
	"regexp"
	"strings"
)

// Kind is a category of sensitive data.
type Kind string

const (
	Card    Kind = "card_number"
	Aadhaar Kind = "aadhaar"
	Secret  Kind = "secret"
	Email   Kind = "email"
	Phone   Kind = "phone"
)

var (
	// 13-19 digits, optionally grouped by spaces or dashes.
	cardRe    = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	aadhaarRe = regexp.MustCompile(`\b[2-9]\d{3}[ -]?\d{4}[ -]?\d{4}\b`)
	emailRe   = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	// Indian mobile numbers, with optional +91 / 0 prefix.
	phoneRe   = regexp.MustCompile(`(?:\+91[ -]?|\b0)?\b[6-9]\d{9}\b`)
	secretRes = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                           // AWS access key id
		regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}\b`),                                   // payment-provider keys
		regexp.MustCompile(`\bsk-ant-[0-9A-Za-z_-]{20,}\b`),                                                  // Anthropic keys
		regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{30,}\b`),                                                 // GitHub tokens
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                                             // private keys
		regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|token|passw(?:or)?d)\s*[:=]\s*["'][^"'\s]{16,}["']`), // key = "..."
	}
	// Placeholders already produced by Redact; never re-flagged.
	placeholderRe = regexp.MustCompile(`\[REDACTED:[a-z_]+\]`)
)

// Finding counts matches per kind.
type Finding map[Kind]int

// Total is the number of matches.
func (f Finding) Total() int {
	n := 0
	for _, c := range f {
		n += c
	}
	return n
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Luhn reports whether a digit string passes the Luhn checksum.
func Luhn(d string) bool {
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum, alt := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

var (
	vD = [10][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
		{3, 4, 0, 1, 2, 8, 9, 5, 6, 7}, {4, 0, 1, 2, 3, 9, 5, 6, 7, 8}, {5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
		{6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3}, {8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
		{9, 8, 7, 6, 5, 4, 3, 2, 1, 0}}
	vP = [8][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
		{8, 9, 1, 6, 0, 4, 3, 5, 2, 7}, {9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
		{2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8}}
)

// Verhoeff reports whether a digit string passes the Verhoeff checksum (used by Aadhaar).
func Verhoeff(d string) bool {
	c := 0
	for i := 0; i < len(d); i++ {
		c = vD[c][vP[i%8][int(d[len(d)-1-i]-'0')]]
	}
	return c == 0
}

func placeholder(k Kind) string { return "[REDACTED:" + string(k) + "]" }

// Redact replaces sensitive values with placeholders and returns the counts.
func Redact(s string) (string, Finding) {
	f := Finding{}
	for _, re := range secretRes {
		s = re.ReplaceAllStringFunc(s, func(string) string { f[Secret]++; return placeholder(Secret) })
	}
	s = aadhaarRe.ReplaceAllStringFunc(s, func(m string) string {
		if d := digits(m); len(d) == 12 && Verhoeff(d) {
			f[Aadhaar]++
			return placeholder(Aadhaar)
		}
		return m
	})
	s = cardRe.ReplaceAllStringFunc(s, func(m string) string {
		if Luhn(digits(m)) {
			f[Card]++
			return placeholder(Card)
		}
		return m
	})
	s = emailRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasSuffix(strings.ToLower(m), ".invalid") || strings.HasSuffix(strings.ToLower(m), "@example.com") {
			return m // reserved, non-routable test domains are not personal data
		}
		f[Email]++
		return placeholder(Email)
	})
	s = phoneRe.ReplaceAllStringFunc(s, func(string) string { f[Phone]++; return placeholder(Phone) })
	return s, f
}

// Scan reports sensitive values without changing anything (placeholders ignored).
func Scan(s string) Finding {
	_, f := Redact(placeholderRe.ReplaceAllString(s, ""))
	return f
}
