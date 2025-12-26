// Package publicip discovers this machine's public IP address by asking a few
// well-known echo services. Several are tried in order so a single provider
// being down or rate limiting does not stall the updater.
package publicip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chriswirz/cf-ddns/internal/xlog"
)

// Family selects which kind of address to look for.
type Family int

const (
	IPv4 Family = iota
	IPv6
)

func (f Family) String() string {
	if f == IPv6 {
		return "IPv6"
	}
	return "IPv4"
}

// network is the Go dial network that forces the right address family. This is
// what actually guarantees we learn our v4 address and not our v6 one (or the
// reverse) when the machine has both.
func (f Family) network() string {
	if f == IPv6 {
		return "tcp6"
	}
	return "tcp4"
}

// Providers are queried in order until one returns a usable address. Each
// returns a bare IP, except the Cloudflare trace endpoints, which are parsed.
//
// The lists are per family because the address family is forced at dial time:
// asking for an IPv6 address over tcp6 can only work if the provider's own
// hostname has an AAAA record, so an IPv4-only host such as api.ipify.org is
// useless there no matter how good our connectivity is. The v6 list is
// therefore restricted to hosts that are dual-stack or v6-only.
var (
	// ProvidersV4 are queried for an A record's content.
	ProvidersV4 = []string{
		"https://cloudflare.com/cdn-cgi/trace",
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
		"https://ifconfig.me/ip",
	}

	// ProvidersV6 are queried for an AAAA record's content.
	ProvidersV6 = []string{
		"https://cloudflare.com/cdn-cgi/trace",
		"https://api64.ipify.org",
		"https://ipv6.icanhazip.com",
		"https://ifconfig.co/ip",
	}
)

// providers returns the list to query for this family.
func (f Family) providers() []string {
	if f == IPv6 {
		return ProvidersV6
	}
	return ProvidersV4
}

// Resolver looks up the public IP for one address family.
type Resolver struct {
	client *http.Client
	family Family
}

// NewResolver builds a resolver pinned to the given address family.
func NewResolver(f Family) *Resolver {
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	return &Resolver{
		family: f,
		client: &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
					return dialer.DialContext(ctx, f.network(), addr)
				},
			},
		},
	}
}

// ErrNoConnectivity is returned when every provider failed before a connection
// was established, which means this host has no route for the family at all
// rather than that the providers are having a bad day. For IPv6 that is the
// common case: plenty of networks are still v4 only.
var ErrNoConnectivity = errors.New("no connectivity for this address family")

// Get returns the current public IP, trying each provider in turn. The error
// reports every provider that failed, since a total failure usually means the
// link is down rather than that one site misbehaved.
func (r *Resolver) Get(ctx context.Context) (string, error) {
	var errs []string
	allDialFailures := true
	for _, url := range r.family.providers() {
		ip, err := r.fetch(ctx, url)
		if err == nil {
			return ip, nil
		}
		if !isDialFailure(err) {
			allDialFailures = false
		}
		errs = append(errs, fmt.Sprintf("%s: %v", url, err))
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// The per-provider detail is a wall of near-identical text, so it goes to
	// the debug log and the returned error stays readable.
	xlog.Debugf("%s lookup failed: %s", r.family, strings.Join(errs, "; "))
	if allDialFailures {
		return "", fmt.Errorf("%w: could not reach any of the %d %s providers, so this host has no %s route (run with --verbose for the per-provider detail)",
			ErrNoConnectivity, len(errs), r.family, r.family)
	}
	return "", fmt.Errorf("no %s address from any of the %d providers (run with --verbose for the per-provider detail)",
		r.family, len(errs))
}

// isDialFailure reports whether err happened before any bytes were exchanged,
// i.e. DNS or connect failed rather than the server answering badly.
func isDialFailure(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func (r *Resolver) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cf-ddns")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	// 4 KiB is plenty for a bare IP or a trace body, and caps a hostile reply.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return "", err
	}
	return parse(string(body), r.family)
}

// parse extracts an address of the wanted family from a provider response,
// handling both a bare IP and the key=value lines of a Cloudflare trace.
func parse(body string, want Family) (string, error) {
	text := strings.TrimSpace(body)
	if i := strings.Index(text, "ip="); i >= 0 {
		rest := text[i+3:]
		if j := strings.IndexAny(rest, "\r\n"); j >= 0 {
			rest = rest[:j]
		}
		text = strings.TrimSpace(rest)
	}
	ip := net.ParseIP(text)
	if ip == nil {
		return "", fmt.Errorf("not an IP address: %q", truncate(text))
	}
	isV4 := ip.To4() != nil
	if (want == IPv4) != isV4 {
		return "", fmt.Errorf("got %s, wanted %s", ip, want)
	}
	return ip.String(), nil
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
