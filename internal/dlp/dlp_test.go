package dlp

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name, in string
		kind     Kind
	}{
		{"visa test card", "card 4111 1111 1111 1111 on file", Card},
		{"card no spaces", "pan=5500005555555559", Card},
		{"aadhaar valid", "aadhaar 2363 4537 3514 here", Aadhaar},
		{"aws key", "key AKIAABCDEFGHIJKLMNOP", Secret},
		{"assigned secret", `api_key = "x9Fq2LmZ7tR4vB8nK1sD6hJ3"`, Secret},
		{"email", "mail ravi.kumar@gmail.com now", Email},
		{"mobile", "call +91 9876543210", Phone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, f := Redact(c.in)
			if f[c.kind] != 1 {
				t.Fatalf("found %v in %q", f, c.in)
			}
			if !strings.Contains(out, "[REDACTED:"+string(c.kind)+"]") {
				t.Errorf("not redacted: %q", out)
			}
		})
	}
}

func TestNoFalsePositives(t *testing.T) {
	for _, s := range []string{
		"amount_paise = 125000",
		"order id 4111111111111112",   // fails Luhn
		"aadhaar-like 2363 4537 3515", // fails Verhoeff
		"commit 9f1c2e7a5b3d4c6e8f0a1b2c3d4e5f6a7b8c9d0e",
		"author aegis-agent@aegis.invalid",
		"timestamp 1732190000",
	} {
		if f := Scan(s); f.Total() != 0 {
			t.Errorf("false positive %v in %q", f, s)
		}
	}
}

func TestChecksums(t *testing.T) {
	if !Luhn("4111111111111111") || Luhn("4111111111111112") {
		t.Error("luhn")
	}
	if !Verhoeff("236345373514") || Verhoeff("236345373515") {
		t.Error("verhoeff")
	}
}

func TestScanIgnoresPlaceholders(t *testing.T) {
	out, _ := Redact("card 4111 1111 1111 1111")
	if Scan(out).Total() != 0 {
		t.Error("placeholder re-flagged")
	}
}
