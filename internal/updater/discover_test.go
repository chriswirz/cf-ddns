package updater

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chriswirz/cf-ddns/internal/cloudflare"
	"github.com/chriswirz/cf-ddns/internal/config"
)

func TestDiscoverWritesPossibleRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/zones":
			io.WriteString(w, `{"success":true,"errors":[],"result":[
				{"id":"z1","name":"example.com"},
				{"id":"z2","name":"example.net"}],
				"result_info":{"page":1,"per_page":100,"count":2,"total_count":2,"total_pages":1}}`)
		case "/zones/z1/dns_records":
			if r.URL.Query().Get("type") == "AAAA" {
				io.WriteString(w, `{"success":true,"errors":[],"result":[
					{"id":"r2","type":"AAAA","name":"home.example.com","content":"2001:db8::1","ttl":1,"proxied":false}],
					"result_info":{"page":1,"per_page":100,"count":1,"total_count":1,"total_pages":1}}`)
				return
			}
			io.WriteString(w, `{"success":true,"errors":[],"result":[
				{"id":"r1","type":"A","name":"home.example.com","content":"198.51.100.1","ttl":300,"proxied":false,"comment":"the NAS"},
				{"id":"r3","type":"A","name":"www.example.com","content":"198.51.100.2","ttl":1,"proxied":true}],
				"result_info":{"page":1,"per_page":100,"count":2,"total_count":2,"total_pages":1}}`)
		case "/zones/z2/dns_records":
			// A zone the token cannot read must not hide the one that worked.
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"success":false,"errors":[{"code":9109,"message":"forbidden"}],"result":null}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	old := cloudflare.BaseURL
	cloudflare.BaseURL = srv.URL
	defer func() { cloudflare.BaseURL = old }()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "// note": "keep me",
  "api_token": "tok",
  "records": []
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CF_API_TOKEN", "")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Discover(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"// note": "keep me"`) {
		t.Errorf("comment key lost:\n%s", out)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config no longer loads: %v\n%s", err, out)
	}
	got := reloaded.PossibleRecords
	if len(got) != 3 {
		t.Fatalf("got %d possible records, want 3:\n%s", len(got), out)
	}
	// Sorted by zone, then name, then type.
	want := []config.Possible{
		{Zone: "example.com", Name: "home.example.com", Type: "A", TTL: 300, Content: "198.51.100.1", Comment: "the NAS"},
		{Zone: "example.com", Name: "home.example.com", Type: "AAAA", TTL: 1, Content: "2001:db8::1"},
		{Zone: "example.com", Name: "www.example.com", Type: "A", TTL: 1, Proxied: true, Content: "198.51.100.2"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// An entry from possible_records must be usable in records with no editing.
func TestPossibleRecordIsAValidRecordEntry(t *testing.T) {
	p := config.Possible{Zone: "example.com", Name: "home.example.com", Type: "AAAA", TTL: 300, Proxied: true}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var r config.Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Zone != p.Zone || r.Name != p.Name || r.Type != p.Type || r.TTL != p.TTL || r.Proxied != p.Proxied {
		t.Errorf("fields did not carry over: %+v -> %+v", p, r)
	}
}

func TestDiscoverNoZones(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"errors":[],"result":[],"result_info":{"page":1,"per_page":100,"count":0,"total_count":0,"total_pages":0}}`)
	}))
	defer srv.Close()
	old := cloudflare.BaseURL
	cloudflare.BaseURL = srv.URL
	defer func() { cloudflare.BaseURL = old }()

	err := Discover(context.Background(), &config.Config{APIToken: "t", Path: "unused"})
	if err == nil || !strings.Contains(err.Error(), "no zones") {
		t.Fatalf("want a 'no zones' error, got %v", err)
	}
}
