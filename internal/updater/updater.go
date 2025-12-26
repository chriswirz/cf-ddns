// Package updater is the loop that runs as the background service: check the
// public IP on an interval and push it to Cloudflare when it has moved.
package updater

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/chriswirz/cf-ddns/internal/cloudflare"
	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/publicip"
	"github.com/chriswirz/cf-ddns/internal/xlog"
)

// Updater keeps a set of DNS records pointed at the current public IP.
type Updater struct {
	cfg *config.Config
	api *cloudflare.Client
	ip  map[publicip.Family]*publicip.Resolver

	// lastIP is the address we last confirmed at Cloudflare, per family. It
	// only lets us skip the API calls; every failure clears it so the next
	// tick re-checks rather than trusting a value we never got to write.
	lastIP map[publicip.Family]string

	// noConnWarned suppresses the repeat of a "this host has no IPv6" error on
	// every tick. A v4-only network is a standing condition, not news, so it
	// is reported once and then only at debug level until it changes.
	noConnWarned map[publicip.Family]bool

	// reloadWarned and emptyWarned do the same for the two ways re-reading the
	// config can go wrong, which are equally standing conditions: a file left
	// unparseable, and one left with no records.
	reloadWarned bool
	emptyWarned  bool
}

// New builds an updater from a loaded config.
func New(cfg *config.Config) *Updater {
	return &Updater{
		cfg: cfg,
		api: cloudflare.New(cfg.APIToken),
		ip: map[publicip.Family]*publicip.Resolver{
			publicip.IPv4: publicip.NewResolver(publicip.IPv4),
			publicip.IPv6: publicip.NewResolver(publicip.IPv6),
		},
		lastIP:       map[publicip.Family]string{},
		noConnWarned: map[publicip.Family]bool{},
	}
}

// Run checks immediately, then every configured interval until ctx is done.
// The config file is re-read before each cycle, so edits apply without a
// restart. Errors are logged and retried on the next tick rather than stopping
// the service, since the usual cause is a link that is briefly down.
func (u *Updater) Run(ctx context.Context) error {
	xlog.Infof("cf-ddns started: %d record(s), checking every %s",
		len(u.cfg.Records), u.cfg.Interval())
	u.logRecords()

	ticker := time.NewTicker(u.cfg.Interval())
	defer ticker.Stop()

	for {
		if err := u.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			xlog.Errorf("%v", err)
		}
		select {
		case <-ctx.Done():
			xlog.Infof("shutting down")
			return nil
		case <-ticker.C:
		}

		// Re-read the config before every cycle, so an edit takes effect
		// without a restart. A read that fails leaves the running config
		// exactly as it was.
		if u.reload() {
			ticker.Reset(u.cfg.Interval())
		}
	}
}

// RunOnce performs a single check-and-update pass over every record.
func (u *Updater) RunOnce(ctx context.Context) error {
	var errs []error
	for _, family := range u.families() {
		recs := u.recordsFor(family)
		if len(recs) == 0 {
			continue
		}
		xlog.Debugf("checking public %s for %d record(s)", family, len(recs))
		ip, err := u.ip[family].Get(ctx)
		if err != nil {
			delete(u.lastIP, family)
			// A family with no route at all is a standing condition; say so
			// once rather than on every tick.
			if errors.Is(err, publicip.ErrNoConnectivity) {
				if u.noConnWarned[family] {
					xlog.Debugf("%v", err)
					continue
				}
				u.noConnWarned[family] = true
			}
			errs = append(errs, err)
			continue
		}
		if u.noConnWarned[family] {
			xlog.Infof("%s connectivity is back", family)
			u.noConnWarned[family] = false
		}
		if prev, ok := u.lastIP[family]; ok && prev == ip {
			xlog.Debugf("public %s is %s (unchanged, %d record(s) already correct)",
				family, ip, len(recs))
			continue
		}
		if _, seen := u.lastIP[family]; !seen {
			xlog.Infof("public %s is %s", family, ip)
		} else {
			xlog.Infof("public %s changed to %s", family, ip)
		}

		ok := true
		for _, r := range recs {
			if err := u.sync(ctx, r, ip); err != nil {
				ok = false
				errs = append(errs, fmt.Errorf("%s (%s): %w", r.Name, r.Type, err))
			}
		}
		if ok {
			u.lastIP[family] = ip
		} else {
			delete(u.lastIP, family)
		}
	}
	return errors.Join(errs...)
}

// families returns the address families actually in use, v4 first.
func (u *Updater) families() []publicip.Family {
	var out []publicip.Family
	for _, f := range []publicip.Family{publicip.IPv4, publicip.IPv6} {
		if len(u.recordsFor(f)) > 0 {
			out = append(out, f)
		}
	}
	return out
}

func (u *Updater) recordsFor(f publicip.Family) []config.Record {
	want := "A"
	if f == publicip.IPv6 {
		want = "AAAA"
	}
	var out []config.Record
	for _, r := range u.cfg.Records {
		if r.Type == want {
			out = append(out, r)
		}
	}
	return out
}

// sameAddr compares two addresses by value rather than by text. It matters for
// IPv6, where one address has many spellings: "2001:db8::1" and
// "2001:0db8:0000:0000:0000:0000:0000:0001" are the same address, and a string
// compare would see a change on every tick and rewrite the record forever.
func sameAddr(a, b string) bool {
	x, errA := netip.ParseAddr(a)
	y, errB := netip.ParseAddr(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return x == y
}

// sync brings one record in line with ip, creating it when configured to.
func (u *Updater) sync(ctx context.Context, r config.Record, ip string) error {
	zoneID, err := u.api.ZoneID(ctx, r.Zone)
	if err != nil {
		return err
	}
	xlog.Debugf("looking up %s %s in zone %s (%s)", r.Type, r.Name, r.Zone, zoneID)
	cur, err := u.api.FindRecord(ctx, zoneID, r.Name, r.Type)
	if err != nil {
		return err
	}
	want := cloudflare.Record{
		Type:    r.Type,
		Name:    r.Name,
		Content: ip,
		TTL:     r.TTL,
		Proxied: r.Proxied,
	}
	if cur == nil {
		if !r.Create {
			return fmt.Errorf("no %s record in zone %s (set \"create\": true to add it)", r.Type, r.Zone)
		}
		if err := u.api.CreateRecord(ctx, zoneID, want); err != nil {
			return err
		}
		xlog.Infof("created %s %s -> %s", r.Type, r.Name, ip)
		return nil
	}
	if sameAddr(cur.Content, ip) && cur.TTL == r.TTL && cur.Proxied == r.Proxied {
		xlog.Debugf("%s %s is already %s, nothing to do", r.Type, r.Name, ip)
		return nil
	}
	xlog.Debugf("%s %s is %s, changing to %s", r.Type, r.Name, cur.Content, ip)
	if err := u.api.UpdateRecord(ctx, zoneID, cur.ID, want); err != nil {
		return err
	}
	xlog.Infof("updated %s %s: %s -> %s", r.Type, r.Name, cur.Content, ip)
	return nil
}
