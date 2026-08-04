package state_test

import (
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
		firstDone <- firstStore.WithDispatchLockAt(sharedLock, func() error {
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
		secondDone <- secondStore.WithDispatchLockAt(sharedLock, func() error {
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
