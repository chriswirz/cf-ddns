package updater

import "testing"

func TestSameAddr(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		// The case that matters: Cloudflare may hand back a different but
		// equivalent spelling of the address we sent.
		{"v6 compressed vs expanded", "2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001", true},
		{"v6 case", "2001:DB8::AB", "2001:db8::ab", true},
		{"v6 different", "2001:db8::1", "2001:db8::2", false},
		{"v4 same", "203.0.113.9", "203.0.113.9", true},
		{"v4 different", "203.0.113.9", "203.0.113.10", false},
		// A v4-mapped v6 literal is not the same record content as the v4 text.
		{"v4 vs v6", "203.0.113.9", "2001:db8::1", false},
		{"unparseable falls back to text", "not-an-ip", "not-an-ip", true},
		{"unparseable differs", "not-an-ip", "other", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameAddr(tc.a, tc.b); got != tc.want {
				t.Errorf("sameAddr(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
