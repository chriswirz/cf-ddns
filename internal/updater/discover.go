package updater

import (
	"context"
	"fmt"
	"sort"

	"github.com/chriswirz/cf-ddns/internal/cloudflare"
	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/xlog"
)

// Discover lists every A and AAAA record the API token can see and writes them
// to the config file's "possible_records" section. It is what runs when
// "records" is empty: rather than failing with nothing to do, the tool shows
// what is available so the operator can copy the ones they want.
func Discover(ctx context.Context, cfg *config.Config) error {
	api := cloudflare.New(cfg.APIToken)

	zones, err := api.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("listing zones: %w", err)
	}
	if len(zones) == 0 {
		return fmt.Errorf("the API token can see no zones; check its Zone:Read permission")
	}

	var found []config.Possible
	for _, z := range zones {
		recs, err := api.ListRecords(ctx, z.ID, "A", "AAAA")
		if err != nil {
			// One unreadable zone should not hide the rest, so log and carry on.
			xlog.Errorf("listing records in %s: %v", z.Name, err)
			continue
		}
		xlog.Infof("zone %s: %d A/AAAA record(s)", z.Name, len(recs))
		for _, r := range recs {
			found = append(found, config.Possible{
				Zone:    z.Name,
				Name:    r.Name,
				Type:    r.Type,
				TTL:     r.TTL,
				Proxied: r.Proxied,
				Content: r.Content,
				Comment: r.Comment,
			})
		}
	}

	// Group by zone, then name, so the written list reads in a sensible order.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Zone != found[j].Zone {
			return found[i].Zone < found[j].Zone
		}
		if found[i].Name != found[j].Name {
			return found[i].Name < found[j].Name
		}
		return found[i].Type < found[j].Type
	})

	if err := config.WritePossibleRecords(cfg.Path, found); err != nil {
		return fmt.Errorf("writing possible_records to %s: %w", cfg.Path, err)
	}
	xlog.Infof("wrote %d possible record(s) to %s", len(found), cfg.Path)
	xlog.Infof("move the ones you want kept up to date into \"records\", then run again")
	return nil
}
