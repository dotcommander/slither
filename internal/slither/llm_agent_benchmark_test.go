package slither

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func BenchmarkBuildReportSourceReads(b *testing.B) {
	repo := b.TempDir()
	for rel, text := range map[string]string{
		"go.mod":              "module example.test/benchmark\n\ngo 1.25\n",
		"main.go":             "package main\nimport \"example.test/benchmark/internal/dep\"\nfunc main() {}\n",
		"internal/dep/dep.go": "package dep\n// TODO: benchmark source reads\nfunc Use() {}\n",
	} {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	previous := sourcePrefixReader
	var reads atomic.Int64
	sourcePrefixReader = func(path string, maxBytes int64) (string, bool, bool, error) {
		reads.Add(1)
		return previous(path, maxBytes)
	}
	b.Cleanup(func() { sourcePrefixReader = previous })
	b.ResetTimer()
	for range b.N {
		if _, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 1 << 10}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(reads.Load())/float64(b.N), "source_reads/op")
}

func BenchmarkContextPacketSourceReads(b *testing.B) {
	b.Skip("Phase C: BuildContextPacket source-read gate is not implemented")
}
