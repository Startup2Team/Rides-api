package waitlist

import "testing"

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"already E.164", "+250788123456", "+250788123456", true},
		{"E.164 with spaces", "+250 788 123 456", "+250788123456", true},
		{"E.164 with dashes", "+250-788-123-456", "+250788123456", true},
		{"Rwanda local format", "0788123456", "+250788123456", true},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"missing plus", "250788123456", "", false},
		{"too short local", "078812345", "", false},
		{"too long local", "07881234567", "", false},
		{"letters", "not-a-phone", "", false},
		{"leading zero but not 10 digits", "012345", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizePhone(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizePhone(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("normalizePhone(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
