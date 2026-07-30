package slither

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentGitSnapshotTransitionsAndReuse(t *testing.T) {
	repo, original := agentGitTestRepo(t, []byte("package fixture\n// TODO: original\nfunc Main() {}\n"))
	snapshots := agentSnapshots{repo: repo}
	if _, err := snapshots.current(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	clean := snapshots.snapshot
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot != clean {
		t.Fatalf("clean reuse = err:%v reused:%t", err, snapshots.snapshot == clean)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: dirty\nfunc Main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot == clean {
		t.Fatalf("clean to dirty = err:%v rebuilt:%t", err, snapshots.snapshot != clean)
	}
	dirty := snapshots.snapshot
	if err := os.WriteFile(filepath.Join(repo, "extra.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot == dirty {
		t.Fatalf("untracked add = err:%v rebuilt:%t", err, snapshots.snapshot != dirty)
	}
	added := snapshots.snapshot
	if err := os.Remove(filepath.Join(repo, "extra.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot == added {
		t.Fatalf("untracked delete = err:%v rebuilt:%t", err, snapshots.snapshot != added)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeClean := snapshots.snapshot
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot == beforeClean {
		t.Fatalf("dirty to clean = err:%v rebuilt:%t", err, snapshots.snapshot != beforeClean)
	}
}

func TestAgentGitDirtyPrefixAndRefreshFailure(t *testing.T) {
	prefix := append([]byte("package fixture\n// "), bytes.Repeat([]byte("x"), int(defaultMaxBytes))...)
	repo, _ := agentGitTestRepo(t, prefix)
	path := filepath.Join(repo, "main.go")
	dirty := append([]byte(nil), prefix...)
	dirty[0] = 'P'
	if err := os.WriteFile(path, dirty, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots := agentSnapshots{repo: repo}
	if _, err := snapshots.current(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	stableDirty := snapshots.snapshot
	dirty[len(dirty)-1] = 'y' // same size, beyond the bounded inspected prefix
	if err := os.WriteFile(path, dirty, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.current(context.Background(), false); err != nil || snapshots.snapshot != stableDirty {
		t.Fatalf("same-size suffix = err:%v reused:%t", err, snapshots.snapshot == stableDirty)
	}

	previous := gitMetadataRunner
	gitMetadataRunner = func(context.Context, string, ...string) (string, error) { return "", errors.New("metadata failed") }
	t.Cleanup(func() { gitMetadataRunner = previous })
	if _, err := snapshots.current(context.Background(), false); err == nil || snapshots.snapshot != nil {
		t.Fatalf("metadata failure = err:%v snapshot:%#v", err, snapshots.snapshot)
	}
	// A failed rebuild also clears a previously retained snapshot.
	gitMetadataRunner = previous
	if _, err := snapshots.current(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshots.current(ctx, false); !errors.Is(err, context.Canceled) || snapshots.snapshot != nil {
		t.Fatalf("failed refresh = err:%v snapshot:%#v", err, snapshots.snapshot)
	}
}

func TestAgentGitRefreshRetriesMutationDuringBuild(t *testing.T) {
	repo, _ := agentGitTestRepo(t, []byte("package fixture\n// TODO: before\nfunc Main() {}\n"))
	previous := agentBuildReport
	calls := 0
	agentBuildReport = func(ctx context.Context, opts Options) (Report, error) {
		report, err := previous(ctx, opts)
		calls++
		if calls == 1 {
			if writeErr := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: after\nfunc Changed() {}\n"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return report, err
	}
	t.Cleanup(func() { agentBuildReport = previous })
	snapshots := agentSnapshots{repo: repo}
	got, err := snapshots.current(context.Background(), false)
	if err != nil || calls != 2 {
		t.Fatalf("mutation retry = err:%v calls:%d", err, calls)
	}
	want, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil || got.ReportID != want.ReportID {
		t.Fatalf("rebuilt report = got:%s want:%s err:%v", got.ReportID, want.ReportID, err)
	}
}

func TestAgentGitFingerprintIncludesRenameSource(t *testing.T) {
	repo, _ := agentGitTestRepo(t, []byte("package fixture\n"))
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package same\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTestCommand(t, repo, "add", "--", "a.go", "b.go")
	runGitTestCommand(t, repo, "commit", "-q", "-m", "identical sources")
	runGitTestCommand(t, repo, "mv", "a.go", "moved.go")
	fromA, err := agentGitFingerprint(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "mv", "moved.go", "a.go")
	runGitTestCommand(t, repo, "mv", "b.go", "moved.go")
	fromB, err := agentGitFingerprint(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if fromA == fromB {
		t.Fatal("rename fingerprints collided across distinct identical origins")
	}
}

func agentGitTestRepo(t *testing.T, main []byte) (string, []byte) {
	t.Helper()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Slither Test")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), main, 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", "main.go")
	runGitTestCommand(t, repo, "commit", "-q", "-m", "initial")
	return repo, main
}
