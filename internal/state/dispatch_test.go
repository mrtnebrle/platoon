package state_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/state"
)

func TestDispatchLockSerializesConcurrentCallers(t *testing.T) {
	parent := t.TempDir()
	firstStore, err := state.Open(filepath.Join(parent, "state-a"))
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := state.Open(filepath.Join(parent, "state-b"))
	if err != nil {
		t.Fatal(err)
	}
	sharedLock := filepath.Join(parent, "sergeant-dispatch.lock")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- firstStore.WithDispatchLockAt(context.Background(), sharedLock, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- secondStore.WithDispatchLockAt(context.Background(), sharedLock, func() error {
			close(secondEntered)
			return nil
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		t.Fatal("second dispatch entered while the first held the global lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second dispatch did not proceed after lock release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestUserAuthorityRootIgnoresProcessDirectoryEnvironment(t *testing.T) {
	first, err := state.UserAuthorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	second, err := state.UserAuthorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("authority root changed with environment: %q != %q", first, second)
	}
}

func TestDispatchLockWaitHonorsContextDeadline(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.WithDispatchLock(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := false
	err = store.WithDispatchLock(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("bounded lock error=%v called=%v", err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
