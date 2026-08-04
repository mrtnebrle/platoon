package state_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/state"
)

func TestAuthorityRejectsCrossRootClaimOverlap(t *testing.T) {
	authority, err := state.OpenAuthorityAt(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repository, alias); err != nil {
		t.Fatal(err)
	}
	key, err := state.RepositoryKey(repository)
	if err != nil {
		t.Fatal(err)
	}
	aliasKey, err := state.RepositoryKey(alias)
	if err != nil {
		t.Fatal(err)
	}
	if key != aliasKey {
		t.Fatalf("repository aliases have different keys: %q %q", key, aliasKey)
	}
	_, err = authority.ReserveClaim(context.Background(), state.GlobalClaim{
		RepositoryKey: key, Mode: manifest.Implementation, Paths: []string{"internal/shared"},
		Semantic: []string{"shared-contract"}, MaxWriters: 2, StateRoot: "state-a", RunID: "run-a", StageID: "stage-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authority.ReserveClaim(context.Background(), state.GlobalClaim{
		RepositoryKey: key, Mode: manifest.Implementation, Paths: []string{"internal/shared/child"},
		Semantic: []string{"other-contract"}, MaxWriters: 2, StateRoot: "state-b", RunID: "run-b", StageID: "stage-b",
	})
	if !errors.Is(err, state.ErrGlobalClaimConflict) {
		t.Fatalf("overlapping claim error = %v", err)
	}
}

func TestAuthorityIntegrationLockSerializesInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	first, err := state.OpenAuthorityAt(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.OpenAuthorityAt(root)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.WithIntegrationLock(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := false
	err = second.WithIntegrationLock(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("integration lock error=%v called=%v", err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
