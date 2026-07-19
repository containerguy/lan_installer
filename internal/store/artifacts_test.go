package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestArtifactMetadataIsImmutableForDigest(t *testing.T) {
	st, err := Open(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip"}); err != nil {
		t.Fatalf("idempotent registration: %v", err)
	}
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 43, ContentType: "application/zip"}); err == nil {
		t.Fatal("conflicting metadata was accepted")
	}
	if _, err = st.Artifact(ctx, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid digest lookup: %v", err)
	}
}
