package slither

import (
	"context"
	"io/fs"
	"os"
	"time"
)

// currentTime keeps report timestamps and stale-marker age calculations under
// one clock. Production uses the wall clock; seam tests replace it serially.
var currentTime = time.Now

// sourcePrefixReader is the bounded, UTF-8-aware source reader used by both
// inspection and score-context construction. It deliberately retains the
// truncation status needed by callers to preserve existing limits.
var sourcePrefixReader = readTextPrefixWithStatus

// contextRoot confines context capsule reads to the repository descriptor.
// Keeping this seam small lets tests prove a path swap cannot escape the root.
type contextRoot interface {
	Open(string) (*os.File, error)
	Close() error
}

var contextRootOpener = func(path string) (contextRoot, error) { return os.OpenRoot(path) }

var contextDescriptorReader = readTextPrefixReaderWithStatus

type outcomeFile interface {
	Stat() (fs.FileInfo, error)
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type outcomeRoot interface {
	Stat(string) (fs.FileInfo, error)
	Lstat(string) (fs.FileInfo, error)
	OpenFile(string, int, fs.FileMode) (outcomeFile, error)
	Close() error
}

type osOutcomeRoot struct{ root *os.Root }

func (root osOutcomeRoot) Stat(name string) (fs.FileInfo, error)  { return root.root.Stat(name) }
func (root osOutcomeRoot) Lstat(name string) (fs.FileInfo, error) { return root.root.Lstat(name) }
func (root osOutcomeRoot) OpenFile(name string, flag int, perm fs.FileMode) (outcomeFile, error) {
	return root.root.OpenFile(name, flag, perm)
}
func (root osOutcomeRoot) Close() error { return root.root.Close() }

var outcomeRootOpener = func(path string) (outcomeRoot, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return osOutcomeRoot{root: root}, nil
}

// outcomeWriter is deliberately path-bound: an implementation is configured
// for one outcome destination before it is passed into later agent work.
type outcomeWriter interface {
	WriteOutcome(context.Context, Report, outcomeFeedback) error
}

type outcomeFeedback struct {
	ReportID    string
	EvidenceID  string
	Verdict     string
	FilesOpened int
	ToolCalls   int
	ReviewMS    int
}

type outcomeWriterFunc func(context.Context, Report, outcomeFeedback) error

func (f outcomeWriterFunc) WriteOutcome(ctx context.Context, report Report, feedback outcomeFeedback) error {
	return f(ctx, report, feedback)
}
