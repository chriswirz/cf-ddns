package publicip

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	trace := "fl=123f\nh=cloudflare.com\nip=203.0.113.7\nts=1700000000\n"
	tests := []struct {
		name string
		body string
		want Family
		out  string
		err  bool
	}{
		{"bare v4", "203.0.113.7\n", IPv4, "203.0.113.7", false},
		{"bare v6", "2001:db8::1\n", IPv6, "2001:db8::1", false},
		{"trace v4", trace, IPv4, "203.0.113.7", false},
		{"wrong family", "203.0.113.7", IPv6, "", true},
		{"html error page", "<html>nope</html>", IPv4, "", true},
		{"empty", "", IPv4, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse(tc.body, tc.want)
			if tc.err {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.out {
				t.Fatalf("got %q, want %q", got, tc.out)
			}
		})
	}
}

func TestParseIPv6Forms(t *testing.T) {
	// Whatever spelling a provider uses, we hand Cloudflare the canonical one.
	for _, in := range []string{
		"2001:0db8:0000:0000:0000:0000:0000:0001",
		"2001:DB8::1",
		"  2001:db8::1\n",
	} {
		got, err := parse(in, IPv6)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != "2001:db8::1" {
			t.Errorf("parse(%q) = %q, want the canonical 2001:db8::1", in, got)
		}
	}
}

func TestParseTraceIPv6(t *testing.T) {
	body := "fl=1a2b\nh=cloudflare.com\nip=2001:db8::dead:beef\nts=1700000000\nvisit_scheme=https\n"
	got, err := parse(body, IPv6)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2001:db8::dead:beef" {
		t.Errorf("got %q", got)
	}
	// The same body must be rejected when an IPv4 address was wanted.
	if _, err := parse(body, IPv4); err == nil {
		t.Error("want an error asking for IPv4 from a v6 trace")
	}
}

func TestProvidersAreDistinctPerFamily(t *testing.T) {
	// The v6 list must not contain hosts that are IPv4 only, since the dial is
	// forced to tcp6 and could never connect to them.
	v4Only := map[string]bool{
		"https://api.ipify.org":      true,
		"https://ipv4.icanhazip.com": true,
		"https://ifconfig.me/ip":     true,
	}
	for _, p := range ProvidersV6 {
		if v4Only[p] {
			t.Errorf("%s is IPv4 only and cannot serve the IPv6 list", p)
		}
	}
	if len(ProvidersV4) == 0 || len(ProvidersV6) == 0 {
		t.Fatal("both provider lists must be non-empty")
	}
	if got := IPv6.providers(); len(got) != len(ProvidersV6) {
		t.Errorf("IPv6.providers() returned the wrong list")
	}
	if got := IPv4.providers(); len(got) != len(ProvidersV4) {
		t.Errorf("IPv4.providers() returned the wrong list")
	}
}

func TestNoConnectivityIsDistinguishable(t *testing.T) {
	// Point the resolver at an address that cannot be dialled, so every
	// provider fails at connect time.
	r := NewResolver(IPv6)
	old := ProvidersV6
	ProvidersV6 = []string{"https://cf-ddns-invalid.invalid/ip"}
	defer func() { ProvidersV6 = old }()

	_, err := r.Get(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, ErrNoConnectivity) {
		t.Errorf("want ErrNoConnectivity, got %v", err)
	}
}

func TestServerErrorIsNotNoConnectivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewResolver(IPv4)
	old := ProvidersV4
	ProvidersV4 = []string{srv.URL}
	defer func() { ProvidersV4 = old }()

	_, err := r.Get(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	// The server answered, so this is not a connectivity problem.
	if errors.Is(err, ErrNoConnectivity) {
		t.Errorf("an HTTP 500 must not be reported as missing connectivity: %v", err)
	}
}
