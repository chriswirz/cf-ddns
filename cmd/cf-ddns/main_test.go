package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs f with stdout redirected and returns what it printed.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	w.Close()
	return <-done
}

func TestAboutReportsVersionAndRepo(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	version, commit, date = "v1.2.3", "abc1234", "2026-01-02T03:04:05Z"

	got := capture(t, about)
	for _, want := range []string{"v1.2.3", "abc1234", "2026-01-02T03:04:05Z", repoURL} {
		if !strings.Contains(got, want) {
			t.Errorf("about() output is missing %q:\n%s", want, got)
		}
	}
}

// A version passed via -ldflags is what the build system says this binary is;
// the build-info fallback must not rewrite it, or a release build would report
// a version the release does not have.
func TestBuildInfoLeavesAnInjectedVersionAlone(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })

	version, commit, date = "v1.2.3", "none", "unknown"
	buildInfo()
	if version != "v1.2.3" {
		t.Errorf("version = %q, want the injected v1.2.3 unchanged", version)
	}
	// The gaps may still be filled from build info, or left at their defaults
	// when the test binary carries no VCS stamp. Either is fine; what must not
	// happen is the version being rewritten.
}

// Without -ldflags the fallback should still produce something more useful
// than "dev", and must not double up the dirty marker that Go's own
// pseudo-version already carries.
func TestBuildInfoFallbackDoesNotDoubleDirty(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })

	version, commit, date = "dev", "none", "unknown"
	buildInfo()
	if n := strings.Count(version, "dirty"); n > 1 {
		t.Errorf("version = %q has the dirty marker %d times", version, n)
	}
}

func TestModeNames(t *testing.T) {
	for m, want := range map[mode]string{
		modeService:  "cf-ddns",
		modeOnce:     "cf-ddns once",
		modeDiscover: "cf-ddns discover",
	} {
		if got := m.name(); got != want {
			t.Errorf("mode %d name = %q, want %q", m, got, want)
		}
	}
}
