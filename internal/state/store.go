package state

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxStateSize = 4 << 20

type Store struct {
	root string
}

func Open(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve state root: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("state root must be a real directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("state root must not be accessible by group or world")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("create state root: %w", err)
		}
		if err := os.Chmod(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("secure state root: %w", err)
		}
	} else {
		return nil, fmt.Errorf("inspect state root: %w", err)
	}
	runs := filepath.Join(absolute, "runs")
	if err := secureDirectory(runs); err != nil {
		return nil, err
	}
	return &Store{root: absolute}, nil
}

func OpenRead(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve state root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect state root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("state root is not a secure real directory")
	}
	return &Store{root: absolute}, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) RunDir(id string) (string, error) {
	if !safeID(id) {
		return "", errors.New("run id is invalid")
	}
	return filepath.Join(s.root, "runs", id), nil
}

func (s *Store) LoadRun(id string) (*Run, error) {
	directory, err := s.RunDir(id)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := readJSON(filepath.Join(directory, "state.json"), &run); err != nil {
		return nil, fmt.Errorf("load run %q: %w", id, err)
	}
	if run.ID != id {
		return nil, errors.New("run state identity does not match its directory")
	}
	if err := run.Validate(); err != nil {
		return nil, fmt.Errorf("validate run %q: %w", id, err)
	}
	expectedIntent := filepath.Join(directory, "intent.md")
	if filepath.Clean(run.IntentPath) != expectedIntent {
		return nil, errors.New("run intent path is not owned by its run directory")
	}
	intent, err := readAuthorityFile(expectedIntent, maxStateSize)
	if err != nil {
		return nil, fmt.Errorf("load run intent: %w", err)
	}
	digest := sha256.Sum256(intent)
	if hex.EncodeToString(digest[:]) != run.IntentRevision {
		return nil, errors.New("run intent digest does not match durable state")
	}
	return &run, nil
}

func (s *Store) SaveRun(run *Run, lease *Lease) error {
	if run == nil || lease == nil {
		return errors.New("run and lease are required")
	}
	if run.Generation != lease.Generation() {
		return ErrFenced
	}
	if err := lease.authorize(); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return fmt.Errorf("refuse invalid run state: %w", err)
	}
	directory, err := s.RunDir(run.ID)
	if err != nil {
		return err
	}
	if err := secureDirectory(directory); err != nil {
		return err
	}
	raw, err := marshalJSON(run)
	if err != nil {
		return fmt.Errorf("encode run state: %w", err)
	}
	if len(raw) > maxStateSize {
		return fmt.Errorf("run state exceeds %d bytes", maxStateSize)
	}
	if err := atomicWrite(filepath.Join(directory, "state.json"), raw, 0o600); err != nil {
		return fmt.Errorf("save run state: %w", err)
	}
	return nil
}

func (s *Store) WriteRunFile(runID, name string, raw []byte, lease *Lease) (string, error) {
	if lease == nil {
		return "", errors.New("commander lease is required")
	}
	if err := lease.authorize(); err != nil {
		return "", err
	}
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", errors.New("run file name is invalid")
	}
	directory, err := s.RunDir(runID)
	if err != nil {
		return "", err
	}
	if err := secureDirectory(directory); err != nil {
		return "", err
	}
	file := filepath.Join(directory, name)
	if err := atomicWrite(file, raw, 0o600); err != nil {
		return "", err
	}
	return file, nil
}

func secureDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s must be a real directory", path)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s must not be accessible by group or world", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create secure directory: %w", err)
	}
	return os.Chmod(path, 0o700)
}

func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func readJSON(path string, destination any) error {
	raw, err := readAuthorityFile(path, maxStateSize)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("authority file contains trailing JSON")
	}
	return nil
}

func readAuthorityFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("authority file must be a regular non-symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("authority file must not be accessible by group or world")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("authority file exceeds %d bytes", limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("authority file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("authority file exceeded its limit while reading")
	}
	return raw, nil
}

func atomicWrite(path string, raw []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refuse to replace non-regular authority file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	suffix, err := randomHex(8)
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp."+suffix)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func randomHex(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
