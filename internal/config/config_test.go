package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "")
	p := write(t, `{"api_token":"tok","records":[{"name":"Home.Example.com."}]}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalSeconds != 300 {
		t.Errorf("interval = %d, want 300", cfg.IntervalSeconds)
	}
	r := cfg.Records[0]
	if r.Name != "home.example.com" || r.Zone != "example.com" || r.Type != "A" || r.TTL != 1 {
		t.Errorf("record not normalized: %+v", r)
	}
}

func TestIntervalFloor(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "")
	p := write(t, `{"api_token":"t","interval_seconds":5,"records":[{"name":"a.example.com"}]}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalSeconds != 30 {
		t.Errorf("interval = %d, want floor of 30", cfg.IntervalSeconds)
	}
}

func TestEnvTokenWins(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "from-env")
	p := write(t, `{"api_token":"from-file","records":[{"name":"a.example.com"}]}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "from-env" {
		t.Errorf("token = %q, want from-env", cfg.APIToken)
	}
}

func TestErrors(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "")
	for name, body := range map[string]string{
		"no token":  `{"records":[{"name":"a.example.com"}]}`,
		"no name":   `{"api_token":"t","records":[{"ttl":60}]}`,
		"bad type":  `{"api_token":"t","records":[{"name":"a.example.com","type":"CNAME"}]}`,
		"bare name": `{"api_token":"t","records":[{"name":"localhost"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// An empty "records" list is deliberately not an error: it is the signal to run
// the discovery pass and write a possible_records section.
func TestEmptyRecordsIsNotAnError(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "")
	p := write(t, `{"api_token":"t","records":[]}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Records) != 0 {
		t.Fatalf("got %d records, want 0", len(cfg.Records))
	}
	if cfg.Path != p {
		t.Errorf("Path = %q, want %q", cfg.Path, p)
	}
}

// The example config is what people copy to config.json, so it has to be
// valid: a stray comment entry inside "records" would fail validation for
// anyone who used it verbatim.
func TestExampleConfigIsValid(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "tok") // the example ships with an empty token
	p := write(t, Example())
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}
	if len(cfg.Records) != 4 {
		t.Errorf("got %d records, want 4", len(cfg.Records))
	}
	var sawAAAA bool
	for _, r := range cfg.Records {
		if r.Name == "" || r.Zone == "" || r.Type == "" {
			t.Errorf("record not fully normalized: %+v", r)
		}
		if r.Type == "AAAA" {
			sawAAAA = true
		}
	}
	if !sawAAAA {
		t.Error("the example should demonstrate an AAAA record")
	}
}
