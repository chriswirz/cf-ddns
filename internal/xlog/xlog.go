// Package xlog is a tiny leveled logger so the rest of the code stays terse.
package xlog

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var verbose atomic.Bool

// Normal output goes to stdout and errors to stderr, so `cf-ddns -v | tee`
// captures the actions while errors stay visible on the terminal. Both are
// redirected together when a log file is configured.
var (
	outMu  sync.Mutex
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// SetVerbose toggles debug output.
func SetVerbose(v bool) { verbose.Store(v) }

// Verbose reports whether debug output is on.
func Verbose() bool { return verbose.Load() }

// SetOutput sends both normal and error output to w, e.g. to a log file when
// running as a service.
func SetOutput(w io.Writer) { SetOutputs(w, w) }

// SetOutputs sets the normal and error destinations separately.
func SetOutputs(normal, errs io.Writer) {
	outMu.Lock()
	defer outMu.Unlock()
	out = normal
	errOut = errs
}

// AddLogFile sends output to w in addition to stdout and stderr, rather than
// instead of them, so a configured log file can never make a run go silent.
// It deliberately does not try to detect whether the console is a terminal:
// piping into `tee` or into a pager is exactly when losing the output is most
// surprising. Under systemd this does mean each line lands in both the journal
// and the file, so leave log_file unset there.
func AddLogFile(w io.Writer) {
	SetOutputs(io.MultiWriter(os.Stdout, w), io.MultiWriter(os.Stderr, w))
}

func ts() string { return time.Now().Format("2006-01-02 15:04:05") }

// emit takes a pointer to the writer variable so a later SetOutput is picked
// up by callers that captured the destination at package level.
func emit(w *io.Writer, level, msg string) {
	outMu.Lock()
	defer outMu.Unlock()
	// A logger has nowhere useful to report a failure to log.
	_, _ = fmt.Fprintf(*w, "%s %s %s\n", ts(), level, msg)
}

// Debugf logs to stdout only when verbose is enabled.
func Debugf(format string, a ...any) {
	if verbose.Load() {
		emit(&out, "DBG", fmt.Sprintf(format, a...))
	}
}

// Infof logs an informational line to stdout.
func Infof(format string, a ...any) { emit(&out, "INF", fmt.Sprintf(format, a...)) }

// Errorf logs an error line to stderr.
func Errorf(format string, a ...any) { emit(&errOut, "ERR", fmt.Sprintf(format, a...)) }

// Fatalf logs and exits non-zero.
func Fatalf(format string, a ...any) {
	emit(&errOut, "FATAL", fmt.Sprintf(format, a...))
	os.Exit(1)
}
