package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/publicip"
)

// loaded writes a config file and returns an Updater built from it, the way
// the service does at startup.
func loaded(t *testing.T, body string) (*Updater, string) {
	t.Helper()
	t.Setenv("CF_API_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, body)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg), path
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const oneRecord = `{"api_token":"tok","interval_seconds":300,
	"records":[{"name":"a.example.com"}]}`

func TestReloadPicksUpAnAddedRecord(t *testing.T) {
	u, path := loaded(t, oneRecord)
	// Pretend a previous cycle confirmed this address, so the short circuit
	// that skips the API is armed.
	u.lastIP[publicip.IPv4] = "203.0.113.1"

	write(t, path, `{"api_token":"tok","interval_seconds":300,
		"records":[{"name":"a.example.com"},{"name":"b.example.com"}]}`)

	if changed := u.reload(); changed {
		t.Error("interval did not change, reload should not say it did")
	}
	if len(u.cfg.Records) != 2 {
		t.Fatalf("got %d records, want 2", len(u.cfg.Records))
	}
	// The new record has to be written even though the address has not moved,
	// so the confirmed-address cache must have been dropped.
	if len(u.lastIP) != 0 {
		t.Errorf("lastIP should be cleared when the records change, got %v", u.lastIP)
	}
}

func TestReloadKeepsPreviousConfigWhenTheFileIsCorrupt(t *testing.T) {
	u, path := loaded(t, oneRecord)
	before := u.cfg

	// A file caught midway through being saved is the case this protects.
	write(t, path, `{"api_token":"tok","records":[{"name":"a.exa`)

	u.reload()
	if u.cfg != before {
		t.Error("a corrupt config replaced the running one")
	}
	if len(u.cfg.Records) != 1 || u.cfg.Records[0].Name != "a.example.com" {
		t.Errorf("records changed: %+v", u.cfg.Records)
	}
	if !u.reloadWarned {
		t.Error("the failure should have been reported once")
	}

	// Fixing the file must be picked up, and clear the warned flag so a later
	// failure is reported again.
	write(t, path, `{"api_token":"tok","interval_seconds":300,
		"records":[{"name":"c.example.com"}]}`)
	u.reload()
	if u.reloadWarned {
		t.Error("reloadWarned should reset once the file parses again")
	}
	if u.cfg.Records[0].Name != "c.example.com" {
		t.Errorf("recovered config not applied: %+v", u.cfg.Records)
	}
}

func TestReloadKeepsPreviousConfigWhenTheFileIsGone(t *testing.T) {
	u, path := loaded(t, oneRecord)
	before := u.cfg

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	u.reload()

	if u.cfg != before {
		t.Error("a missing config replaced the running one")
	}
	if len(u.cfg.Records) != 1 {
		t.Errorf("records changed: %+v", u.cfg.Records)
	}
}

// An empty records list means "go and discover" at startup, but partway
// through a run it is much more likely to be a file caught mid-edit, and
// acting on it would silently stop DNS being updated.
func TestReloadKeepsPreviousRecordsWhenTheNewListIsEmpty(t *testing.T) {
	u, path := loaded(t, oneRecord)

	write(t, path, `{"api_token":"tok","records":[]}`)
	u.reload()

	if len(u.cfg.Records) != 1 {
		t.Fatalf("got %d records, want the previous 1", len(u.cfg.Records))
	}
	if !u.emptyWarned {
		t.Error("the empty list should have been reported once")
	}
}

func TestReloadReportsAnIntervalChange(t *testing.T) {
	u, path := loaded(t, oneRecord)

	write(t, path, `{"api_token":"tok","interval_seconds":900,
		"records":[{"name":"a.example.com"}]}`)

	if !u.reload() {
		t.Fatal("reload should report that the interval changed")
	}
	if got := u.cfg.Interval().Seconds(); got != 900 {
		t.Errorf("interval = %v, want 900s", got)
	}
}

func TestReloadReplacesTheClientWhenTheTokenChanges(t *testing.T) {
	u, path := loaded(t, oneRecord)
	before := u.api
	u.lastIP[publicip.IPv4] = "203.0.113.1"

	write(t, path, `{"api_token":"rotated","interval_seconds":300,
		"records":[{"name":"a.example.com"}]}`)
	u.reload()

	if u.api == before {
		t.Error("the API client should be rebuilt when the token changes")
	}
	// A different token may reach different zones, so nothing previously
	// confirmed can be trusted.
	if len(u.lastIP) != 0 {
		t.Errorf("lastIP should be cleared on a token change, got %v", u.lastIP)
	}
}

// Nothing changed means nothing is disturbed: in particular the confirmed
// address survives, or every cycle would make needless API calls.
func TestReloadOfAnUnchangedFileKeepsTheCache(t *testing.T) {
	u, _ := loaded(t, oneRecord)
	u.lastIP[publicip.IPv4] = "203.0.113.1"

	if u.reload() {
		t.Error("nothing changed, so the interval cannot have")
	}
	if u.lastIP[publicip.IPv4] != "203.0.113.1" {
		t.Errorf("lastIP was cleared for an unchanged config: %v", u.lastIP)
	}
}

// A config assembled in memory has no file behind it, and reload must not
// treat that as an error.
func TestReloadIsANoOpWithoutAPath(t *testing.T) {
	u := New(&config.Config{
		APIToken:        "t",
		IntervalSeconds: 300,
		Records:         []config.Record{{Zone: "example.com", Name: "a.example.com", Type: "A", TTL: 1}},
	})
	if u.reload() {
		t.Error("reload should do nothing without a path")
	}
	if u.reloadWarned {
		t.Error("no path is not a failure to report")
	}
}
