package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

var (
	ErrLeaseHeld      = errors.New("commander lease is held")
	ErrLeaseAmbiguous = errors.New("commander lease owner cannot be proven stale")
	ErrFenced         = errors.New("commander generation is fenced")
)

const leaseVersion = "platoon.lease/v1alpha1"

type LeaseOptions struct {
	TTL        time.Duration
	Now        func() time.Time
	Hostname   string
	PID        int
	OwnerAlive func(host string, pid int) (bool, error)
}

type leaseRecord struct {
	Version    string    `json:"version"`
	Status     string    `json:"status"`
	Generation uint64    `json:"generation"`
	Token      string    `json:"token"`
	Hostname   string    `json:"hostname"`
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquiredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type Lease struct {
	store   *Store
	lock    *os.File
	record  leaseRecord
	options LeaseOptions
	closed  bool
}

func (s *Store) AcquireLease(options LeaseOptions) (*Lease, error) {
	resolved, err := resolveLeaseOptions(options)
	if err != nil {
		return nil, err
	}
	lockPath := s.root + string(os.PathSeparator) + ".commander.lock"
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("commander lock must be a regular non-symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open commander lock: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, ErrLeaseHeld
	}
	unlocked := false
	defer func() {
		if !unlocked {
			return
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	previous, err := s.readLeaseRecord()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		unlocked = true
		return nil, fmt.Errorf("read commander lease: %w", err)
	}
	now := resolved.Now().UTC()
	generation := uint64(1)
	if err == nil {
		generation = previous.Generation + 1
		if previous.Status == "held" {
			if now.Before(previous.ExpiresAt) {
				unlocked = true
				return nil, ErrLeaseHeld
			}
			if previous.Hostname != resolved.Hostname {
				unlocked = true
				return nil, ErrLeaseAmbiguous
			}
			alive, probeErr := resolved.OwnerAlive(previous.Hostname, previous.PID)
			if probeErr != nil {
				unlocked = true
				return nil, fmt.Errorf("%w: process probe failed", ErrLeaseAmbiguous)
			}
			if alive {
				unlocked = true
				return nil, ErrLeaseHeld
			}
		} else if previous.Status != "released" {
			unlocked = true
			return nil, ErrLeaseAmbiguous
		}
	}
	token, err := randomHex(16)
	if err != nil {
		unlocked = true
		return nil, err
	}
	record := leaseRecord{
		Version: leaseVersion, Status: "held", Generation: generation, Token: token,
		Hostname: resolved.Hostname, PID: resolved.PID, AcquiredAt: now, ExpiresAt: now.Add(resolved.TTL),
	}
	if err := s.writeLeaseRecord(record); err != nil {
		unlocked = true
		return nil, err
	}
	return &Lease{store: s, lock: lock, record: record, options: resolved}, nil
}

func (l *Lease) Generation() uint64 {
	if l == nil {
		return 0
	}
	return l.record.Generation
}

func (l *Lease) Renew() error {
	if l == nil || l.closed {
		return ErrFenced
	}
	current, err := l.store.readLeaseRecord()
	if err != nil {
		return ErrFenced
	}
	if current.Status != "held" || current.Generation != l.record.Generation || current.Token != l.record.Token {
		return ErrFenced
	}
	now := l.options.Now().UTC()
	if !now.Before(current.ExpiresAt) {
		return ErrFenced
	}
	current.ExpiresAt = now.Add(l.options.TTL)
	if err := l.store.writeLeaseRecord(current); err != nil {
		return err
	}
	l.record = current
	return nil
}

func (l *Lease) authorize() error {
	return l.Renew()
}

func (l *Lease) Release() error {
	if l == nil || l.closed {
		return nil
	}
	defer l.unlock()
	current, err := l.store.readLeaseRecord()
	if err != nil {
		return err
	}
	if current.Status != "held" || current.Generation != l.record.Generation || current.Token != l.record.Token {
		return ErrFenced
	}
	current.Status = "released"
	current.ExpiresAt = l.options.Now().UTC()
	return l.store.writeLeaseRecord(current)
}

func (l *Lease) crash() {
	if l != nil {
		l.unlock()
	}
}

func (l *Lease) unlock() {
	if l.closed {
		return
	}
	l.closed = true
	_ = syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
	_ = l.lock.Close()
}

func (s *Store) readLeaseRecord() (leaseRecord, error) {
	var record leaseRecord
	if err := readJSON(s.root+string(os.PathSeparator)+"lease.json", &record); err != nil {
		return leaseRecord{}, err
	}
	if record.Version != leaseVersion || record.Generation == 0 || record.Token == "" || record.Hostname == "" || record.PID <= 0 {
		return leaseRecord{}, errors.New("commander lease record is invalid")
	}
	return record, nil
}

func (s *Store) writeLeaseRecord(record leaseRecord) error {
	raw, err := marshalJSON(record)
	if err != nil {
		return err
	}
	return atomicWrite(s.root+string(os.PathSeparator)+"lease.json", raw, 0o600)
}

func resolveLeaseOptions(options LeaseOptions) (LeaseOptions, error) {
	if options.TTL == 0 {
		options.TTL = 5 * time.Minute
	}
	if options.TTL < time.Second || options.TTL > 24*time.Hour {
		return LeaseOptions{}, errors.New("lease TTL must be from 1s through 24h")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			return LeaseOptions{}, errors.New("determine lease hostname")
		}
		options.Hostname = hostname
	}
	if options.PID == 0 {
		options.PID = os.Getpid()
	}
	if options.PID < 1 {
		return LeaseOptions{}, errors.New("lease PID must be positive")
	}
	if options.OwnerAlive == nil {
		options.OwnerAlive = localProcessAlive
	}
	return options, nil
}

func localProcessAlive(_ string, pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
