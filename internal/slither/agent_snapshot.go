package slither

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type agentSnapshot struct {
	report      Report
	fingerprint string
}

type agentFingerprintEntryValue struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Source  string `json:"source,omitempty"`
	State   string `json:"state"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

var agentBuildReport = BuildReport

type agentSnapshots struct {
	repo     string
	snapshot *agentSnapshot
}

func (s *agentSnapshots) current(ctx context.Context, force bool) (Report, error) {
	if !force && s.snapshot != nil && s.snapshot.report.SourceState.Kind == "git" {
		fingerprint, err := agentGitFingerprint(ctx, s.repo)
		if err == nil && fingerprint == s.snapshot.fingerprint {
			return s.snapshot.report, nil
		}
	}
	// A failed refresh must never make a stale report look current.
	s.snapshot = nil
	return s.rebuild(ctx)
}

func (s *agentSnapshots) rebuild(ctx context.Context) (Report, error) {
	for attempt := 0; attempt < 2; attempt++ {
		before, beforeErr := agentGitFingerprint(ctx, s.repo)
		report, err := agentBuildReport(ctx, Options{Repo: s.repo, Top: defaultTop, MaxBytes: defaultMaxBytes, Days: defaultDays, NoCache: true})
		if err != nil {
			return Report{}, err
		}
		if report.SourceState.Kind != "git" {
			s.snapshot = &agentSnapshot{report: report}
			return report, nil
		}
		if beforeErr != nil {
			return Report{}, beforeErr
		}
		after, err := agentGitFingerprint(ctx, s.repo)
		if err != nil {
			return Report{}, err
		}
		if before == after {
			s.snapshot = &agentSnapshot{report: report, fingerprint: after}
			return report, nil
		}
	}
	return Report{}, errAgentSnapshotChanged
}

func agentGitFingerprint(ctx context.Context, repo string) (string, error) {
	head, err := gitMetadataRunner(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil && !isUnbornGitRepository(ctx, repo) {
		return "", fmt.Errorf("read git head: %w", err)
	}
	status, err := gitMetadataRunner(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", fmt.Errorf("read git status: %w", err)
	}
	if status == "" {
		return sha256Identity("slither.agent.snapshot/v1", []string{"clean", strings.TrimSpace(head)}), nil
	}
	entries := make([]agentFingerprintEntryValue, 0)
	parts := strings.Split(status, "\x00")
	for index := 0; index < len(parts); index++ {
		line := parts[index]
		if len(line) < 4 {
			continue
		}
		code, rel := line[:2], line[3:]
		source := ""
		if strings.ContainsAny(code, "RC") && index+1 < len(parts) {
			index++ // porcelain -z supplies the original name as the next record.
			source = parts[index]
		}
		entries = append(entries, agentFingerprintEntry(repo, code, rel, source))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Status < entries[j].Status
	})
	return sha256Identity("slither.agent.snapshot/v1", struct {
		Head    string                       `json:"head"`
		Entries []agentFingerprintEntryValue `json:"entries"`
	}{strings.TrimSpace(head), entries}), nil
}

func agentFingerprintEntry(repo, status, rel, source string) agentFingerprintEntryValue {
	entry := agentFingerprintEntryValue{Path: filepath.ToSlash(rel), Status: status, Source: filepath.ToSlash(source), State: "missing"}
	full := filepath.Join(repo, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return entry
	}
	prefix, truncated, err := agentFingerprintPrefix(full, defaultMaxBytes)
	if err != nil {
		entry.State = "unreadable"
		return entry
	}
	entry.State = "regular"
	entry.Size = info.Size()
	entry.Content = contentIdentity(prefix, info.Size(), truncated)
	return entry
}

func agentFingerprintPrefix(path string, maxBytes int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	return data[:min(len(data), int(maxBytes))], len(data) > int(maxBytes), nil
}
