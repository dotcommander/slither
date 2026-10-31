package slither

import (
	"os"
	"sync"
	"time"
)

// prefixReader is the bounded source reader shared by context and inspection.
type prefixReader func(string, int64) (string, bool, bool, error)

type reportSourceKey struct {
	path  string
	limit int64
}

type reportSourceValue struct {
	text      string
	ok        bool
	truncated bool
	size      int64
	modified  time.Time
}

// reportSources keeps only a small amount of source text for the lifetime of
// one report. A changed or vanished file is always read again by inspection.
type reportSources struct {
	mu      sync.Mutex
	entries map[reportSourceKey]reportSourceValue
	bytes   int64
}

func newReportSources() *reportSources {
	return &reportSources{entries: make(map[reportSourceKey]reportSourceValue)}
}

func (sources *reportSources) read(path string, limit int64) (string, bool, bool, error) {
	key := reportSourceKey{path: path, limit: limit}
	info, statErr := os.Stat(path)
	if statErr == nil && info.Mode().IsRegular() {
		sources.mu.Lock()
		cached, found := sources.entries[key]
		sources.mu.Unlock()
		if found && cached.size == info.Size() && cached.modified.Equal(info.ModTime()) {
			return cached.text, cached.ok, cached.truncated, nil
		}
	}
	text, ok, truncated, err := sourcePrefixReader(path, limit)
	if err != nil || statErr != nil || !info.Mode().IsRegular() || !ok {
		return text, ok, truncated, err
	}
	// Never retain large files or grow report-local memory with repository size.
	const maxRetainedSourceBytes = 8 << 20
	if int64(len(text)) > maxRetainedSourceBytes {
		return text, ok, truncated, nil
	}
	// Verify the file did not change during the read; inspection revalidates too.
	after, statErr := os.Stat(path)
	if statErr != nil || !after.Mode().IsRegular() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return text, ok, truncated, nil
	}
	sources.mu.Lock()
	if prior, found := sources.entries[key]; found {
		sources.bytes -= int64(len(prior.text))
		delete(sources.entries, key)
	}
	if sources.bytes+int64(len(text)) <= maxRetainedSourceBytes && len(sources.entries) < 4096 {
		sources.entries[key] = reportSourceValue{text: text, ok: ok, truncated: truncated, size: info.Size(), modified: info.ModTime()}
		sources.bytes += int64(len(text))
	}
	sources.mu.Unlock()
	return text, ok, truncated, nil
}
