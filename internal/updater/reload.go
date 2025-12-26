package updater

import (
	"reflect"
	"strings"

	"github.com/chriswirz/cf-ddns/internal/cloudflare"
	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/publicip"
	"github.com/chriswirz/cf-ddns/internal/xlog"
)

// reload re-reads the config file and applies whatever changed, so an edit
// takes effect on the next cycle instead of needing a restart.
//
// Anything that goes wrong leaves the running config untouched. That is the
// point rather than a fallback: the likeliest reason a read fails is that the
// file is midway through being saved, and a half-written config must never be
// able to stop a working service from updating DNS.
//
// It reports whether the check interval changed, which is the one setting the
// caller has to act on, because the ticker has to be reset.
func (u *Updater) reload() (intervalChanged bool) {
	// A config built in memory rather than read from a file has nothing to
	// re-read; tests do this, and so would an embedded use.
	if u.cfg.Path == "" {
		return false
	}

	fresh, err := config.Load(u.cfg.Path)
	if err != nil {
		// Once, not on every cycle: a file left broken would otherwise repeat
		// the same line every interval for as long as it stayed broken.
		if !u.reloadWarned {
			xlog.Errorf("re-reading %s failed, carrying on with the config already loaded: %v",
				u.cfg.Path, err)
			u.reloadWarned = true
		}
		return false
	}
	if u.reloadWarned {
		xlog.Infof("%s is readable again", u.cfg.Path)
		u.reloadWarned = false
	}

	// An empty records list is meaningful at startup, where it triggers the
	// discovery pass, but partway through a run it is far more likely to be a
	// file caught mid-edit. Keeping the previous records is the safe reading:
	// quietly ceasing to update DNS is the worst outcome available here.
	if len(fresh.Records) == 0 {
		if !u.emptyWarned {
			xlog.Errorf("%s now lists no records, carrying on with the previous %d; "+
				"run `cf-ddns discover` if you meant to look up what is available",
				u.cfg.Path, len(u.cfg.Records))
			u.emptyWarned = true
		}
		return false
	}
	u.emptyWarned = false

	old := u.cfg
	u.cfg = fresh

	if fresh.APIToken != old.APIToken {
		u.api = cloudflare.New(fresh.APIToken)
		// A different token may reach a different set of zones, so nothing
		// already confirmed can be assumed to still hold.
		u.forgetLastIP()
		xlog.Infof("API token changed, reconnecting")
	}

	if !reflect.DeepEqual(fresh.Records, old.Records) {
		// Forget what was last confirmed, or a record added while the address
		// has not moved would not be written until the next time it does.
		u.forgetLastIP()
		xlog.Infof("records changed: now %d record(s)", len(fresh.Records))
		u.logRecords()
	}

	if fresh.Verbose != old.Verbose {
		xlog.SetVerbose(fresh.Verbose)
		xlog.Infof("verbose logging %s", onOff(fresh.Verbose))
	}

	if fresh.LogFile != old.LogFile {
		// main owns the log file handle, so swapping it is a restart rather
		// than something to do partway through a loop.
		xlog.Infof("log_file changed to %q; restart cf-ddns for that to take effect", fresh.LogFile)
	}

	if fresh.IntervalSeconds != old.IntervalSeconds {
		xlog.Infof("check interval changed to %s", fresh.Interval())
		intervalChanged = true
	}

	return intervalChanged
}

// forgetLastIP drops the confirmed-at-Cloudflare cache so the next cycle
// checks every record rather than short-circuiting on an unchanged address.
func (u *Updater) forgetLastIP() {
	u.lastIP = map[publicip.Family]string{}
}

// logRecords lists the configured records per family, at debug level.
func (u *Updater) logRecords() {
	for _, f := range u.families() {
		recs := u.recordsFor(f)
		names := make([]string, 0, len(recs))
		for _, r := range recs {
			names = append(names, r.Name)
		}
		xlog.Debugf("%s records: %s", f, strings.Join(names, ", "))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
