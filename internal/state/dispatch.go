package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func UserDispatchLockPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf(".platoon-sergeant-dispatch-%d.lock", os.Getuid()))
}

func (s *Store) WithDispatchLock(dispatch func() error) error {
	return s.WithDispatchLockAt(filepath.Join(s.root, ".dispatch.lock"), dispatch)
}

func (s *Store) WithDispatchLockAt(lockPath string, dispatch func() error) error {
	if dispatch == nil {
		return errors.New("dispatch operation is required")
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
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return dispatch()
}
