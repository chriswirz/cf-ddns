// Package config loads the JSON file that lists which Cloudflare DNS records
// to keep pointed at this machine's current public IP address.
//
// The API token may be given in the file or, preferably, in the CF_API_TOKEN
// environment variable, which always wins over the file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultName is the config file looked for when --config is not given.
const DefaultName = "config.json"

// Record is one DNS record to keep in sync with the current public IP.
type Record struct {
	// Zone is the zone name, e.g. "example.com". Optional: when empty it is
	// derived from Name by taking the last two labels.
	Zone string `json:"zone,omitempty"`
	// Name is the full record name, e.g. "home.example.com".
	Name string `json:"name"`
	// TTL in seconds; 1 means "automatic". Defaults to 1.
	TTL int `json:"ttl,omitempty"`
	// Proxied turns Cloudflare's orange cloud on for this record.
	Proxied bool `json:"proxied,omitempty"`
	// Type is "A" (IPv4) or "AAAA" (IPv6). Defaults to "A".
	Type string `json:"type,omitempty"`
	// Create makes the record if it does not already exist in the zone.
	Create bool `json:"create,omitempty"`
}

// Config is the top-level config file.
type Config struct {
	APIToken        string   `json:"api_token,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	LogFile         string   `json:"log_file,omitempty"`
	Verbose         bool     `json:"verbose,omitempty"`
	Records         []Record `json:"records"`

	// PossibleRecords is filled in by the tool, not by hand: when "records" is
	// empty, every A and AAAA record the token can see is listed here so you
	// can move the ones you want into "records".
	PossibleRecords []Possible `json:"possible_records,omitempty"`

	// Path is where this config was read from, so the discovery pass can
	// write the possible_records section back to the same file.
	Path string `json:"-"`
}

// Interval is how long to wait between checks.
func (c *Config) Interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}

// DefaultPath is the config file used when --config is not given: one next to
// the executable if present, otherwise the per-user config directory, then the
// current directory. Keeping the executable-relative path first means a
// portable install (unzip anywhere, point a service at it) just works.
func DefaultPath() string {
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), DefaultName); exists(p) {
			return p
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		if p := filepath.Join(dir, "cf-ddns", DefaultName); exists(p) {
			return p
		}
	}
	if runtime.GOOS != "windows" {
		if p := "/etc/cf-ddns/config.json"; exists(p) {
			return p
		}
	}
	return DefaultName
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{IntervalSeconds: 300}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg.Path = path
	if tok := os.Getenv("CF_API_TOKEN"); tok != "" {
		cfg.APIToken = tok
	}
	if err := cfg.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// A relative log path is resolved against the config file's directory so
	// the service's working directory does not matter.
	if cfg.LogFile != "" && !filepath.IsAbs(cfg.LogFile) {
		if dir, err := filepath.Abs(filepath.Dir(path)); err == nil {
			cfg.LogFile = filepath.Join(dir, cfg.LogFile)
		}
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	if c.APIToken == "" {
		return fmt.Errorf("no API token: set api_token or the CF_API_TOKEN environment variable")
	}
	// An empty "records" is not an error: the caller runs the discovery pass
	// and writes a possible_records section for the operator to choose from.
	// Cloudflare rate limits at 1200 requests per five minutes; 30s between
	// checks is far below that and still fast enough for a home connection.
	if c.IntervalSeconds < 30 {
		c.IntervalSeconds = 30
	}
	for i := range c.Records {
		r := &c.Records[i]
		r.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Name), "."))
		if r.Name == "" {
			return fmt.Errorf("record %d: name is required", i)
		}
		if r.Type == "" {
			r.Type = "A"
		}
		r.Type = strings.ToUpper(r.Type)
		if r.Type != "A" && r.Type != "AAAA" {
			return fmt.Errorf("record %s: type must be A or AAAA, got %q", r.Name, r.Type)
		}
		if r.TTL == 0 {
			r.TTL = 1
		}
		if r.Zone == "" {
			z, err := guessZone(r.Name)
			if err != nil {
				return fmt.Errorf("record %s: %w", r.Name, err)
			}
			r.Zone = z
		}
		r.Zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Zone), "."))
	}
	return nil
}

// guessZone takes the last two labels of a name. It is only a convenience for
// plain domains; multi-part suffixes such as example.co.uk need an explicit
// "zone" field.
func guessZone(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot derive zone, set \"zone\" explicitly")
	}
	return strings.Join(parts[len(parts)-2:], "."), nil
}

// Example is a starter config printed by `cf-ddns example-config`.
func Example() string {
	return `{
  "// api_token": "Cloudflare API token with Zone:Read and DNS:Edit on the zones below.",
  "// api_token_env": "Leave empty here and set CF_API_TOKEN instead to keep the secret out of the file.",
  "api_token": "",

  "// interval_seconds": "How often to check the public IP. Minimum 30, default 300.",
  "interval_seconds": 300,

  "// log_file": "Optional, off by default: output goes to the console. Set a path to also append there. Relative paths resolve next to this config file.",
  "log_file": "",

  "// verbose": "Log every check, not just changes. Same as passing --verbose.",
  "verbose": false,

  "// records": "Leave this empty and run cf-ddns to have it fill in a possible_records section listing every A/AAAA record your token can see.",
  "// records_ipv6": "type is A (IPv4) or AAAA (IPv6), default A. List a name twice, once of each, to keep both up to date.",
  "records": [
    { "name": "home.example.com" },
    { "name": "vpn.example.com", "ttl": 60, "proxied": false },
    { "name": "www.example.com", "zone": "example.com", "proxied": true },
    { "name": "home.example.com", "type": "AAAA", "create": true }
  ]
}
`
}
