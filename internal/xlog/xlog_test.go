package xlog

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// swap redirects both streams and restores them afterwards.
func swap(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var o, e bytes.Buffer
	oldOut, oldErr := out, errOut
	out, errOut = &o, &e
	t.Cleanup(func() { out, errOut = oldOut, oldErr })
	return &o, &e
}

func TestNormalOutputGoesToStdoutAndErrorsToStderr(t *testing.T) {
	o, e := swap(t)
	SetVerbose(false)
	t.Cleanup(func() { SetVerbose(false) })

	Infof("updated %s", "home.example.com")
	Errorf("boom %d", 1)

	if !strings.Contains(o.String(), "INF updated home.example.com") {
		t.Errorf("stdout = %q", o.String())
	}
	if strings.Contains(o.String(), "boom") {
		t.Errorf("an error leaked into stdout: %q", o.String())
	}
	if !strings.Contains(e.String(), "ERR boom 1") {
		t.Errorf("stderr = %q", e.String())
	}
}

func TestDebugRequiresVerbose(t *testing.T) {
	o, _ := swap(t)
	SetVerbose(false)
	Debugf("quiet")
	if o.Len() != 0 {
		t.Fatalf("debug logged while quiet: %q", o.String())
	}
	if Verbose() {
		t.Error("Verbose() should be false")
	}

	SetVerbose(true)
	t.Cleanup(func() { SetVerbose(false) })
	Debugf("loud %s", "now")
	if !strings.Contains(o.String(), "DBG loud now") {
		t.Errorf("stdout = %q", o.String())
	}
	if !Verbose() {
		t.Error("Verbose() should be true")
	}
}

// The regression this guards: AddLogFile used to replace the console rather
// than add to it, so a config with a log_file made an interactive run silent,
// including its fatal errors. A run that fails must always say so on screen.
func TestAddLogFileKeepsWritingToTheConsole(t *testing.T) {
	var console, file bytes.Buffer
	// Stand in for os.Stdout/os.Stderr, which AddLogFile wires in directly.
	oldOut, oldErr := out, errOut
	t.Cleanup(func() { out, errOut = oldOut, oldErr })
	SetOutputs(io.MultiWriter(&console, &file), io.MultiWriter(&console, &file))

	Infof("checked")
	Errorf("failed")

	for _, w := range []struct {
		name string
		got  string
	}{{"console", console.String()}, {"file", file.String()}} {
		if !strings.Contains(w.got, "checked") {
			t.Errorf("%s missing the info line: %q", w.name, w.got)
		}
		if !strings.Contains(w.got, "failed") {
			t.Errorf("%s missing the error line: %q", w.name, w.got)
		}
	}
}

func TestAddLogFileWiresUpBothDestinations(t *testing.T) {
	oldOut, oldErr := out, errOut
	t.Cleanup(func() { out, errOut = oldOut, oldErr })

	var file bytes.Buffer
	AddLogFile(&file)

	Infof("into the file and the console")
	Errorf("also both")

	got := file.String()
	if !strings.Contains(got, "into the file and the console") || !strings.Contains(got, "also both") {
		t.Errorf("log file did not receive both streams: %q", got)
	}
	// Both destinations must be multi-writers, not the bare file, or the
	// console half has been dropped again.
	if out == io.Writer(&file) || errOut == io.Writer(&file) {
		t.Error("AddLogFile replaced the console instead of adding to it")
	}
}

func TestSetOutputCapturesBothStreams(t *testing.T) {
	swap(t)
	var f bytes.Buffer
	oldOut, oldErr := out, errOut
	SetOutput(io.Writer(&f))
	t.Cleanup(func() { out, errOut = oldOut, oldErr })

	Infof("to the file")
	Errorf("also to the file")

	got := f.String()
	if !strings.Contains(got, "to the file") || !strings.Contains(got, "also to the file") {
		t.Errorf("log file did not capture both streams: %q", got)
	}
}
