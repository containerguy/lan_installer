package deviceclient

import (
	"context"
	"strings"
	"testing"
)

// A signed release supplies the digest, so a hostile or buggy one must not be
// able to steer the download at a different path.
func TestFetchArtifactRejectsUnusableDigest(t *testing.T) {
	fetcher := artifactFetcher{client: &Client{}}
	for _, digest := range []string{
		"", "not-hex", strings.Repeat("a", 63), strings.Repeat("a", 65),
		strings.Repeat("A", 64), "../../etc/passwd",
		strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31),
	} {
		if err := fetcher.FetchArtifact(context.Background(), digest, 10, t.TempDir()+"/out"); err == nil {
			t.Fatalf("digest %q was accepted", digest)
		}
	}
}

func TestFetchArtifactRejectsNonPositiveSize(t *testing.T) {
	fetcher := artifactFetcher{client: &Client{}}
	for _, size := range []int64{0, -1} {
		if err := fetcher.FetchArtifact(context.Background(), strings.Repeat("a", 64), size, t.TempDir()+"/out"); err == nil {
			t.Fatalf("size %d was accepted", size)
		}
	}
}
