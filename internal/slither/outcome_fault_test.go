package slither

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOutcomeWriterRejectsWriteSyncAndCloseFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		file testOutcomeFile
	}{
		{"short write", testOutcomeFile{short: true}},
		{"write", testOutcomeFile{writeErr: errors.New("write")}},
		{"sync", testOutcomeFile{syncErr: errors.New("sync")}},
		{"close", testOutcomeFile{closeErr: errors.New("close")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, report, feedback := testBoundOutcomeWriter(t, &test.file)
			if err := writer.WriteOutcome(context.Background(), report, feedback); err == nil {
				t.Fatal("write failure was accepted")
			}
		})
	}
}

func TestOutcomeWriterRejectsConcurrentCreation(t *testing.T) {
	parent := testOutcomeInfo{mode: fs.ModeDir | 0o700}
	previous := outcomeRootOpener
	outcomeRootOpener = func(string) (outcomeRoot, error) {
		return testOutcomeRoot{parent: parent, lstatErr: fs.ErrNotExist, openErr: fs.ErrExist}, nil
	}
	t.Cleanup(func() { outcomeRootOpener = previous })
	if _, err := outcomeWriterForPath(filepath.Join(t.TempDir(), "outcomes.jsonl")); err == nil {
		t.Fatal("concurrent ledger creation was accepted")
	}
}

func TestOutcomeWriterRejectsEveryIrregularTargetMode(t *testing.T) {
	parent := testOutcomeInfo{mode: fs.ModeDir | 0o700}
	for _, mode := range []fs.FileMode{fs.ModeNamedPipe | 0o600, fs.ModeSocket | 0o600, fs.ModeDevice | 0o600, fs.ModeCharDevice | 0o600, fs.ModeSymlink | 0o777, fs.ModeDir | 0o700} {
		previous := outcomeRootOpener
		outcomeRootOpener = func(string) (outcomeRoot, error) {
			return testOutcomeRoot{parent: parent, target: testOutcomeInfo{mode: mode}}, nil
		}
		if _, err := outcomeWriterForPath(filepath.Join(t.TempDir(), "outcomes.jsonl")); err == nil {
			t.Fatalf("irregular mode %v was accepted", mode)
		}
		outcomeRootOpener = previous
	}
}

func TestOutcomeWriterRejectsTargetAndParentReplacement(t *testing.T) {
	report := outcomeTestReport("a", "b")
	feedback := outcomeFeedback{ReportID: report.ReportID, EvidenceID: report.Rows[0].EvidenceID, Verdict: "confirmed"}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, parent, target string)
	}{
		{"target", func(t *testing.T, _, target string) {
			replacement := target + ".new"
			if err := os.WriteFile(replacement, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, target); err != nil {
				t.Fatal(err)
			}
		}},
		{"parent", func(t *testing.T, parent, target string) {
			old := parent + ".old"
			if err := os.Rename(parent, old); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, filepath.Base(target)), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "ledger")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "outcomes.jsonl")
			writer, err := outcomeWriterForPath(target)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, parent, target)
			if err := writer.WriteOutcome(context.Background(), report, feedback); err == nil {
				t.Fatal("replacement was accepted")
			}
		})
	}
}

func TestOutcomeWriterMultipleAppendsAndCancellation(t *testing.T) {
	report := outcomeTestReport("c", "d")
	feedback := outcomeFeedback{ReportID: report.ReportID, EvidenceID: report.Rows[0].EvidenceID, Verdict: "confirmed"}
	path := filepath.Join(t.TempDir(), "outcomes.jsonl")
	writer, err := outcomeWriterForPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteOutcome(context.Background(), report, feedback); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteOutcome(context.Background(), report, feedback); err != nil {
		t.Fatal(err)
	}
	if got := len(bytesSplitLines(readTestFile(t, path))); got != 2 {
		t.Fatalf("records = %d, want 2", got)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := fileHash(t, path)
	if err := writer.WriteOutcome(canceled, report, feedback); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write = %v", err)
	}
	if after := fileHash(t, path); after != before {
		t.Fatal("canceled write mutated ledger")
	}
}

type testOutcomeRoot struct {
	parent, target fs.FileInfo
	file           outcomeFile
	lstatErr       error
	openErr        error
}

type testOutcomeInfo struct{ mode fs.FileMode }

func (info testOutcomeInfo) Name() string       { return "test" }
func (info testOutcomeInfo) Size() int64        { return 0 }
func (info testOutcomeInfo) Mode() fs.FileMode  { return info.mode }
func (info testOutcomeInfo) ModTime() time.Time { return time.Time{} }
func (info testOutcomeInfo) IsDir() bool        { return info.mode.IsDir() }
func (info testOutcomeInfo) Sys() any           { return nil }

func (root testOutcomeRoot) Stat(string) (fs.FileInfo, error)  { return root.parent, nil }
func (root testOutcomeRoot) Lstat(string) (fs.FileInfo, error) { return root.target, root.lstatErr }
func (root testOutcomeRoot) OpenFile(string, int, fs.FileMode) (outcomeFile, error) {
	return root.file, root.openErr
}
func (root testOutcomeRoot) Close() error { return nil }

type testOutcomeFile struct {
	info                        fs.FileInfo
	short                       bool
	writeErr, syncErr, closeErr error
}

func (file *testOutcomeFile) Stat() (fs.FileInfo, error) { return file.info, nil }
func (file *testOutcomeFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	if file.short {
		return len(data) - 1, nil
	}
	return len(data), nil
}
func (file *testOutcomeFile) Sync() error  { return file.syncErr }
func (file *testOutcomeFile) Close() error { return file.closeErr }

func testBoundOutcomeWriter(t *testing.T, file *testOutcomeFile) (outcomeWriter, Report, outcomeFeedback) {
	t.Helper()
	parent := t.TempDir()
	target := filepath.Join(parent, "outcomes.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	file.info = targetInfo
	previous := outcomeRootOpener
	outcomeRootOpener = func(string) (outcomeRoot, error) {
		return testOutcomeRoot{parent: parentInfo, target: targetInfo, file: file}, nil
	}
	t.Cleanup(func() { outcomeRootOpener = previous })
	writer, err := outcomeWriterForPath(target)
	if err != nil {
		t.Fatal(err)
	}
	report := outcomeTestReport("e", "f")
	return writer, report, outcomeFeedback{ReportID: report.ReportID, EvidenceID: report.Rows[0].EvidenceID, Verdict: "confirmed"}
}

func bytesSplitLines(data []byte) [][]byte {
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	return lines
}
