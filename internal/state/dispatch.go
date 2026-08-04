package state

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func UserAuthorityRoot() (string, error) {
	uid := strconv.Itoa(os.Getuid())
	account, err := user.LookupId(uid)
	if err != nil || account.HomeDir == "" {
		return "", errors.New("resolve local user authority home")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(account.HomeDir, "Library", "Application Support", "Platoon", "authority-v1", uid), nil
	}
	return filepath.Join(account.HomeDir, ".local", "state", "platoon", "authority-v1", uid), nil
}

func UserDispatchLockPath() (string, error) {
	root, err := UserAuthorityRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "dispatch.lock"), nil
}

func (s *Store) WithDispatchLock(ctx context.Context, dispatch func() error) error {
	return s.WithDispatchLockAt(ctx, filepath.Join(s.root, ".dispatch.lock"), dispatch)
}

func (s *Store) WithDispatchLockAt(ctx context.Context, lockPath string, dispatch func() error) error {
	return withFileLock(ctx, lockPath, dispatch)
}

func withFileLock(ctx context.Context, lockPath string, operation func() error) error {
	if operation == nil {
		return errors.New("locked operation is required")
	}
	path, err := filepath.Abs(lockPath)
	if err != nil {
		return err
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return errors.New("dispatch lock parent must be a real directory")
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("dispatch lock must be a regular non-symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return err
	}
	opened, err := lock.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return errors.New("dispatch lock changed while opening")
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}
