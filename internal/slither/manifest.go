package slither

import (
	"io"
	"os"
	"path/filepath"
)

const maxRepoManifestBytes int64 = 1 << 20

// readRepoManifest reads a repository manifest only when the complete file fits
// within the manifest safety cap. Callers must treat missing, unreadable, and
// oversized manifests as unavailable rather than parsing a partial document.
func readRepoManifest(repo, rel string) ([]byte, bool) {
	if repo == "" {
		return nil, false
	}

	file, err := os.Open(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxRepoManifestBytes+1))
	if err != nil || int64(len(data)) > maxRepoManifestBytes {
		return nil, false
	}
	return data, true
}
