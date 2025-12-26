// Command cf-ddns keeps Cloudflare DNS records pointed at this machine's
// current public IP address, the way a dynamic-DNS client does.
//
// Usage:
//
//	cf-ddns [--config PATH] [--verbose]  run the update loop (the service mode)
//	cf-ddns once [--config PATH]     check and update once, then exit
//	cf-ddns discover [--config PATH] list the account's A/AAAA records into the
//	                                 config file's "possible_records" section
//	cf-ddns example-config           print a starter config.json
//	cf-ddns about                    version, build details and the repo link
//	cf-ddns version
//
// It is a single static binary with no dependencies, so the same source builds
// for Windows, Linux and macOS on amd64 and arm64. See README.md for how to
// register it as a service on each platform.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/chriswirz/cf-ddns/internal/config"
	"github.com/chriswirz/cf-ddns/internal/updater"
	"github.com/chriswirz/cf-ddns/internal/xlog"
)

// repoURL is where to report a problem or get a newer build.
const repoURL = "https://github.com/chriswirz/cf-ddns"

// Build metadata, injected via -ldflags by the build scripts and by the
// release pipeline. The defaults are what a plain "go build" leaves behind.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildInfo fills in what -ldflags did not. A binary produced by "go install"
// carries no ldflags, but the toolchain does stamp the module version and the
// VCS revision into the build info, so there is still something to report.
func buildInfo() {
	// A version supplied by -ldflags is authoritative: the build scripts and
	// the release pipeline already say exactly what this is, so leave it be.
	injected := version != "dev"

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if !injected && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if commit == "none" {
				commit = setting.Value
			}
		case "vcs.time":
			if date == "unknown" {
				date = setting.Value
			}
		case "vcs.modified":
			// Go's own pseudo-version already ends in "+dirty" when the tree
			// was modified, so only add the marker when it is not there.
			if !injected && setting.Value == "true" && !strings.Contains(version, "dirty") {
				version += "-dirty"
			}
		}
	}
}

// about prints the version, how this binary was built, and where it came from.
func about() {
	fmt.Printf("cf-ddns %s\n", version)
	fmt.Printf("  commit:   %s\n", commit)
	fmt.Printf("  built:    %s\n", date)
	fmt.Printf("  platform: %s/%s (%s)\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Printf("  repo:     %s\n", repoURL)
}

func main() {
	buildInfo()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "-version", "--version":
		// Terse and stable, so a script can parse it.
		fmt.Printf("cf-ddns %s\n", version)
	case "about", "-about", "--about":
		about()
	case "example-config", "--example-config":
		fmt.Print(config.Example())
	case "-h", "--help", "help":
		usage()
	case "once":
		run(args[1:], modeOnce)
	case "discover":
		run(args[1:], modeDiscover)
	default:
		// No subcommand (or a leading flag) means the normal service mode.
		if cmd != "" && cmd[0] != '-' {
			fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", cmd)
			usage()
			os.Exit(2)
		}
		run(args, modeService)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cf-ddns - point Cloudflare DNS records at your current public IP

  cf-ddns [--config PATH] [--verbose]
                                   run continuously (service mode)
  cf-ddns once [--config PATH]     update once and exit
  cf-ddns discover [--config PATH] write every A/AAAA record the token can see
                                   to the config's "possible_records" section
  cf-ddns example-config           print a starter config.json
  cf-ddns about                    version, build details and the repo link
  cf-ddns version                  just the version

--verbose (or -v, or "verbose": true in the config) logs every check, not just
changes. Normal output goes to stdout and errors to stderr.

The Cloudflare API token is read from the CF_API_TOKEN environment variable if
set, otherwise from "api_token" in the config file.

With an empty "records" list, every mode runs discovery instead: it fills in
"possible_records" so you can move the records you want into "records".

`)
	fmt.Fprintf(os.Stderr, "cf-ddns %s - %s\n", version, repoURL)
}

// mode selects what run does after loading the config.
type mode int

const (
	modeService mode = iota
	modeOnce
	modeDiscover
)

func (m mode) name() string {
	switch m {
	case modeOnce:
		return "cf-ddns once"
	case modeDiscover:
		return "cf-ddns discover"
	}
	return "cf-ddns"
}

func run(args []string, m mode) {
	name := m.name()
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("config", "", "path to config.json (default: next to the binary, then the user config dir)")
	// Both spellings, since --verbose is what the config key is called.
	verbose := fs.Bool("verbose", false, "log every check to stdout, not just changes")
	fs.BoolVar(verbose, "v", false, "shorthand for --verbose")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *path == "" {
		*path = config.DefaultPath()
	}

	cfg, err := config.Load(*path)
	if err != nil {
		xlog.Fatalf("%v", err)
	}
	if *verbose || cfg.Verbose {
		xlog.SetVerbose(true)
		// Says plainly that the flag arrived, so "verbose shows nothing" is
		// immediately distinguishable from "verbose was never switched on".
		xlog.Debugf("verbose logging on (cf-ddns %s, %s/%s)", version, runtime.GOOS, runtime.GOARCH)
	}
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			xlog.Fatalf("opening log file %s: %v", cfg.LogFile, err)
		}
		defer func() { _ = f.Close() }()
		// Added to the console, not swapped for it: a log file must not make
		// an interactive run silent, least of all when it fails.
		xlog.AddLogFile(f)
	}
	xlog.Debugf("config loaded from %s", *path)

	// With nothing to update, list what is there instead of idling forever.
	// This is also what `discover` does on demand, so an operator can refresh
	// the list after adding records in the dashboard.
	if m == modeDiscover || len(cfg.Records) == 0 {
		if m != modeDiscover {
			xlog.Infof("no records configured; listing what this token can see")
		}
		if err := updater.Discover(context.Background(), cfg); err != nil {
			xlog.Fatalf("%v", err)
		}
		return
	}

	u := updater.New(cfg)
	if m == modeOnce {
		if err := u.RunOnce(context.Background()); err != nil {
			xlog.Fatalf("%v", err)
		}
		return
	}

	// Ctrl-C, `systemctl stop` and the Windows service stop all arrive here.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := u.Run(ctx); err != nil {
		xlog.Fatalf("%v", err)
	}
}
