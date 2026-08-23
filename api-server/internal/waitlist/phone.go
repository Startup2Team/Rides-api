package waitlist

import (
	"regexp"
	"strings"
)

var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// normalizePhone accepts a phone number already in E.164 form (e.g.
// +250788123456) or Rwandan local form (e.g. 0788123456, 10 digits starting
// with 0) and returns it normalized to E.164. Anything else is rejected —
// this is a public unauthenticated endpoint, so we validate strictly rather
// than guess at ambiguous formats (Uganda numbers also start with 07...,
// which is why we don't attempt to infer a country from a bare local number
// outside the Rwanda-shaped case).
func normalizePhone(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "-", "")
	if p == "" {
		return "", false
	}

	if e164Pattern.MatchString(p) {
		return p, true
	}

	if len(p) == 10 && strings.HasPrefix(p, "0") {
		candidate := "+250" + p[1:]
		if e164Pattern.MatchString(candidate) {
			return candidate, true
		}
	}

	return "", false
}
