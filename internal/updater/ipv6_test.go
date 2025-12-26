package updater

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chriswirz/cf-ddns/internal/cloudflare"
	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/publicip"
)

// cfStub serves a zone holding one AAAA record with the given content, and
// records any PATCH body it receives.
func cfStub(t *testing.T, existing string, patches *[]map[string]any) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/zones":
			io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"z1","name":"example.com"}]}`)
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			body, err := json.Marshal(map[string]any{
				"success": true, "errors": []any{},
				"result": []cloudflare.Record{{
					ID: "r1", Type: "AAAA", Name: "home.example.com",
					Content: existing, TTL: 1, Proxied: false,
				}},
			})
			if err != nil {
				t.Error(err)
			}
			w.Write(body)
		case r.Method == http.MethodPatch:
			var got map[string]any
			json.NewDecoder(r.Body).Decode(&got)
			*patches = append(*patches, got)
			io.WriteString(w, `{"success":true,"errors":[],"result":{}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	old := cloudflare.BaseURL
	cloudflare.BaseURL = srv.URL
	return func() {
		cloudflare.BaseURL = old
		srv.Close()
	}
}

func aaaaUpdater(t *testing.T) *Updater {
	t.Helper()
	cfg := &config.Config{
		APIToken:        "tok",
		IntervalSeconds: 300,
		Records: []config.Record{
			{Zone: "example.com", Name: "home.example.com", Type: "AAAA", TTL: 1},
		},
	}
	return New(cfg)
}

// The regression this guards: Cloudflare returning the expanded spelling of the
// address we just wrote must not look like a change, or the service rewrites
// the same record on every single tick, forever.
func TestIPv6EquivalentSpellingIsNotAnUpdate(t *testing.T) {
	var patches []map[string]any
	defer cfStub(t, "2001:0db8:0000:0000:0000:0000:0000:0001", &patches)()

	u := aaaaUpdater(t)
	if err := u.sync(context.Background(), u.cfg.Records[0], "2001:db8::1"); err != nil {
		t.Fatal(err)
	}
	if len(patches) != 0 {
		t.Fatalf("record was rewritten despite being unchanged: %v", patches)
	}
}

func TestIPv6RealChangeIsWritten(t *testing.T) {
	var patches []map[string]any
	defer cfStub(t, "2001:db8::1", &patches)()

	u := aaaaUpdater(t)
	if err := u.sync(context.Background(), u.cfg.Records[0], "2001:db8::2"); err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(patches))
	}
	if patches[0]["content"] != "2001:db8::2" || patches[0]["type"] != "AAAA" {
		t.Errorf("patch body = %v", patches[0])
	}
}

// AAAA records must be driven by the IPv6 resolver, and A records by the IPv4
// one; a mix-up would write a v4 address into an AAAA record.
func TestRecordsAreSplitByFamily(t *testing.T) {
	cfg := &config.Config{
		APIToken: "t", IntervalSeconds: 300,
		Records: []config.Record{
			{Zone: "example.com", Name: "home.example.com", Type: "A", TTL: 1},
			{Zone: "example.com", Name: "home.example.com", Type: "AAAA", TTL: 1},
			{Zone: "example.com", Name: "vpn.example.com", Type: "AAAA", TTL: 1},
		},
	}
	u := New(cfg)

	if got := u.recordsFor(publicip.IPv4); len(got) != 1 || got[0].Type != "A" {
		t.Errorf("IPv4 records = %+v", got)
	}
	if got := u.recordsFor(publicip.IPv6); len(got) != 2 {
		t.Errorf("IPv6 records = %+v", got)
	}
	fams := u.families()
	if len(fams) != 2 || fams[0] != publicip.IPv4 || fams[1] != publicip.IPv6 {
		t.Errorf("families() = %v, want v4 then v6", fams)
	}

	// With only AAAA records, the v4 resolver must never be consulted.
	cfg.Records = cfg.Records[1:]
	u = New(cfg)
	if fams := u.families(); len(fams) != 1 || fams[0] != publicip.IPv6 {
		t.Errorf("families() = %v, want IPv6 only", fams)
	}
}
