package config

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestWritePossibleRecordsPreservesOrderAndComments(t *testing.T) {
	orig := `{
  "// api_token": "a comment key",
  "api_token": "tok",
  "interval_seconds": 600,
  "records": []
}
`
	p := write(t, orig)
	recs := []Possible{
		{Zone: "example.com", Name: "home.example.com", Type: "A", TTL: 1, Content: "203.0.113.9"},
	}
	if err := WritePossibleRecords(p, recs); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if !json.Valid(out) {
		t.Fatalf("rewritten config is not valid JSON:\n%s", got)
	}
	if !strings.Contains(got, `"// api_token": "a comment key"`) {
		t.Errorf("comment key was dropped:\n%s", got)
	}
	// Original keys must keep their original relative order.
	iTok := strings.Index(got, `"api_token"`)
	iInt := strings.Index(got, `"interval_seconds"`)
	iRec := strings.Index(got, `"records"`)
	iPos := strings.Index(got, `"possible_records"`)
	if iTok > iInt || iInt > iRec || iRec > iPos {
		t.Errorf("keys out of order (tok=%d int=%d rec=%d pos=%d):\n%s", iTok, iInt, iRec, iPos, got)
	}

	// And it must still load, with the original values intact.
	t.Setenv("CF_API_TOKEN", "")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "tok" || cfg.IntervalSeconds != 600 {
		t.Errorf("values changed: %+v", cfg)
	}
	if len(cfg.PossibleRecords) != 1 || cfg.PossibleRecords[0].Content != "203.0.113.9" {
		t.Errorf("possible_records = %+v", cfg.PossibleRecords)
	}
}

func TestWritePossibleRecordsIsIdempotent(t *testing.T) {
	p := write(t, `{"api_token":"t","records":[]}`)
	recs := []Possible{{Zone: "example.com", Name: "a.example.com", Type: "A", TTL: 1}}
	for i := 0; i < 3; i++ {
		if err := WritePossibleRecords(p, recs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	out, _ := os.ReadFile(p)
	if n := strings.Count(string(out), `"possible_records"`); n != 1 {
		t.Errorf("possible_records appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(string(out), `"// possible_records"`); n != 1 {
		t.Errorf("help key appears %d times, want 1:\n%s", n, out)
	}
}

func TestWritePossibleRecordsEmptyList(t *testing.T) {
	p := write(t, `{"api_token":"t","records":[]}`)
	if err := WritePossibleRecords(p, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	if !strings.Contains(string(out), `"possible_records": []`) {
		t.Errorf("want an empty array, got:\n%s", out)
	}
}

func TestWritePossibleRecordsKeepsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows only tracks the read-only bit, not Unix permission bits")
	}
	p := write(t, `{"api_token":"t","records":[]}`)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePossibleRecords(p, nil); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestTopLevelKeysRejectsNonObject(t *testing.T) {
	if _, err := topLevelKeys([]byte(`[1,2,3]`)); err == nil {
		t.Fatal("want error for a JSON array")
	}
}
