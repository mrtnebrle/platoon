package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/planner"
)

const authorityVersion = "platoon.authority/v1alpha1"

var ErrGlobalClaimConflict = errors.New("global repository claim conflicts with active work")

type Authority struct {
	root string
}

type GlobalClaim struct {
	ID            string        `json:"id"`
	RepositoryKey string        `json:"repositoryKey"`
	Mode          manifest.Mode `json:"mode"`
	Paths         []string      `json:"paths"`
	Semantic      []string      `json:"semantic"`
	MaxWriters    int           `json:"maxWriters"`
	StateRoot     string        `json:"stateRoot"`
	RunID         string        `json:"runId"`
	StageID       string        `json:"stageId"`
	Adopted       bool          `json:"adopted"`
}

type authorityRegistry struct {
	Version string        `json:"version"`
	UID     int           `json:"uid"`
	Claims  []GlobalClaim `json:"claims"`
}

func OpenUserAuthority() (*Authority, error) {
	root, err := UserAuthorityRoot()
	if err != nil {
		return nil, err
	}
	return OpenAuthorityAt(root)
}

func OpenAuthorityAt(root string) (*Authority, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("global authority root is not a secure real directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return nil, errors.New("global authority root is not owned by the current user")
	}
	return &Authority{root: absolute}, nil
}

func (a *Authority) Root() string { return a.root }

func (a *Authority) WithDispatchLock(ctx context.Context, operation func() error) error {
	return withFileLock(ctx, filepath.Join(a.root, "dispatch.lock"), operation)
}

func (a *Authority) WithIntegrationLock(ctx context.Context, operation func() error) error {
	return withFileLock(ctx, filepath.Join(a.root, "integration.lock"), operation)
}

func (a *Authority) ReserveClaim(ctx context.Context, claim GlobalClaim) (string, error) {
	if !safeID(claim.RunID) || !safeID(claim.StageID) || claim.RepositoryKey == "" || claim.MaxWriters < 1 {
		return "", errors.New("global claim is invalid")
	}
	if claim.ID == "" {
		claim.ID = globalClaimID(claim.StateRoot, claim.RunID, claim.StageID)
	}
	var conflict error
	err := withFileLock(ctx, filepath.Join(a.root, "registry.lock"), func() error {
		registry, err := a.loadRegistry()
		if err != nil {
			return err
		}
		alreadyRegistered := false
		for _, existing := range registry.Claims {
			if existing.ID == claim.ID {
				alreadyRegistered = true
				break
			}
		}
		writers := 0
		writerLimit := claim.MaxWriters
		candidate := planner.ActiveClaim{Stage: claim.StageID, Repository: claim.RepositoryKey, Mode: claim.Mode, Paths: claim.Paths, Semantic: claim.Semantic}
		for _, existing := range registry.Claims {
			if existing.ID == claim.ID {
				continue
			}
			if existing.RepositoryKey != claim.RepositoryKey || existing.Mode != manifest.Implementation {
				continue
			}
			writers++
			if existing.MaxWriters < writerLimit {
				writerLimit = existing.MaxWriters
			}
			active := planner.ActiveClaim{Stage: existing.StageID, Repository: existing.RepositoryKey, Mode: existing.Mode, Paths: existing.Paths, Semantic: existing.Semantic}
			if overlaps, reason := planner.ActiveClaimsConflict(candidate, active); overlaps {
				conflict = fmt.Errorf("%w: %s", ErrGlobalClaimConflict, reason)
			}
		}
		if claim.Mode == manifest.Implementation && writers+1 > writerLimit {
			conflict = fmt.Errorf("%w: global repository writer limit reached", ErrGlobalClaimConflict)
		}
		if conflict != nil && !claim.Adopted {
			return conflict
		}
		if !alreadyRegistered {
			registry.Claims = append(registry.Claims, claim)
		}
		sort.Slice(registry.Claims, func(i, j int) bool { return registry.Claims[i].ID < registry.Claims[j].ID })
		return a.saveRegistry(registry)
	})
	if err != nil {
		return "", err
	}
	return claim.ID, conflict
}

func (a *Authority) ReleaseClaim(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return withFileLock(ctx, filepath.Join(a.root, "registry.lock"), func() error {
		registry, err := a.loadRegistry()
		if err != nil {
			return err
		}
		filtered := registry.Claims[:0]
		for _, claim := range registry.Claims {
			if claim.ID != id {
				filtered = append(filtered, claim)
			}
		}
		registry.Claims = filtered
		return a.saveRegistry(registry)
	})
}

func RepositoryKey(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository path is not a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("repository filesystem identity is unavailable")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)))
	return hex.EncodeToString(sum[:]), nil
}

func globalClaimID(stateRoot, runID, stageID string) string {
	sum := sha256.Sum256([]byte(stateRoot + "\x00" + runID + "\x00" + stageID))
	return hex.EncodeToString(sum[:16])
}

func (a *Authority) loadRegistry() (authorityRegistry, error) {
	path := filepath.Join(a.root, "registry.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return authorityRegistry{Version: authorityVersion, UID: os.Getuid(), Claims: []GlobalClaim{}}, nil
	}
	var registry authorityRegistry
	if err := readJSON(path, &registry); err != nil {
		return authorityRegistry{}, err
	}
	if registry.Version != authorityVersion || registry.UID != os.Getuid() || registry.Claims == nil {
		return authorityRegistry{}, errors.New("global claim registry is invalid")
	}
	return registry, nil
}

func (a *Authority) saveRegistry(registry authorityRegistry) error {
	raw, err := marshalJSON(registry)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.root, "registry.json"), raw, 0o600)
}
