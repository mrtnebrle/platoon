package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/missioncontrol"
)

type MissionSources struct {
	manifest *manifest.Manifest
	executor Executor
	now      func() time.Time
}

func NewMissionSourceRegistry(m *manifest.Manifest, executor Executor, now func() time.Time) missioncontrol.SourceRegistry {
	sources := &MissionSources{manifest: m, executor: executor, now: now}
	return missioncontrol.SourceRegistry{
		ByKind: map[string]missioncontrol.SourceQuerier{
			"git": sources, "dagr": sources, "platoon-policy": sources, "validation-capability": sources,
		},
		Now: now,
	}
}

func (s *MissionSources) Query(ctx context.Context, query missioncontrol.SourceQuery) (missioncontrol.SourceObservation, error) {
	if s.manifest == nil || s.executor == nil {
		return missioncontrol.SourceObservation{}, errors.New("source adapter is unavailable")
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	base := missioncontrol.SourceObservation{
		SourceID: query.SourceID, Kind: query.Kind, Schema: query.Schema, Revision: query.ExpectedRevision,
		Quality: missioncontrol.QualityVerified, ObservedAt: now.Format(time.RFC3339Nano),
	}
	switch query.Kind {
	case "git":
		base.AdapterVersion = "git-v1"
		repository, ok := s.manifest.Repository(query.Locator)
		if !ok {
			return unavailableObservation(base), nil
		}
		requestedRevision := query.ExpectedRevision
		if requestedRevision == "" {
			requestedRevision = "HEAD"
		}
		result, err := s.executor.Run(ctx, Invocation{
			Executable: "git", Args: []string{"-C", repository.Path, "rev-parse", "--verify", requestedRevision},
			Timeout: s.manifest.Spec.Limits.CommandDuration(), MaxOutput: 4096,
		})
		objectID := strings.TrimSpace(string(result.Stdout))
		if err != nil || len(result.Stderr) != 0 || (len(objectID) != 40 && len(objectID) != 64) ||
			(query.ExpectedRevision != "" && objectID != query.ExpectedRevision) {
			return unavailableObservation(base), nil
		}
		base.Revision = objectID
		base.Payload = map[string]any{"repository": query.Locator, "objectId": objectID}
	case "dagr":
		executableHash, err := executableDigest(s.manifest.Spec.Adapters.Dagr.Executable)
		if err != nil {
			return unavailableObservation(base), nil
		}
		base.AdapterVersion = "dagr-" + executableHash[:16]
		info, err := os.Lstat(s.manifest.Spec.Adapters.Dagr.Database)
		if err != nil || !info.Mode().IsRegular() {
			return unavailableObservation(base), nil
		}
		identity := fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
		result, err := s.executor.Run(ctx, Invocation{
			Executable: s.manifest.Spec.Adapters.Dagr.InspectExecutable,
			Args:       []string{"-readonly", "-batch", "-noheader", s.manifest.Spec.Adapters.Dagr.Database, "PRAGMA user_version;"},
			Timeout:    s.manifest.Spec.Limits.CommandDuration(), MaxOutput: 4096,
		})
		schemaVersion := strings.TrimSpace(string(result.Stdout))
		if err != nil || len(result.Stderr) != 0 {
			return unavailableObservation(base), nil
		}
		if _, err := strconv.ParseUint(schemaVersion, 10, 64); err != nil {
			return unavailableObservation(base), nil
		}
		base.Payload = map[string]any{
			"databaseIdentity": sourceDigest("dagr-database", identity), "schemaVersion": schemaVersion,
			"operations": []string{"ack", "list", "load", "start", "watch"},
		}
		if base.Revision == "" {
			base.Revision = sourceDigest(base.AdapterVersion, identity, schemaVersion)
		}
	case "platoon-policy":
		base.AdapterVersion = "platoon-v1"
		policyBytes, err := json.Marshal(struct {
			Schema string          `json:"schema"`
			Limits manifest.Limits `json:"limits"`
		}{Schema: "platoon.policy/v1alpha1", Limits: s.manifest.Spec.Limits})
		if err != nil {
			return unavailableObservation(base), nil
		}
		policyDigest := sourceDigest(string(policyBytes))
		base.Payload = map[string]any{"policyDigest": policyDigest}
		if base.Revision == "" {
			base.Revision = policyDigest
		}
	case "validation-capability":
		base.AdapterVersion = "unsupported-v1"
		base.Quality = missioncontrol.QualityUnsupported
		base.Payload = map[string]any{"status": "unsupported"}
	default:
		return unavailableObservation(base), nil
	}
	return base, nil
}

func executableDigest(executable string) (string, error) {
	path := executable
	if !strings.ContainsRune(executable, os.PathSeparator) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return "", err
		}
		path = resolved
	}
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return "", errors.New("source executable is unavailable or oversized")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(handle, 64<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func unavailableObservation(observation missioncontrol.SourceObservation) missioncontrol.SourceObservation {
	if observation.Revision == "" {
		observation.Revision = "unavailable"
	}
	observation.Quality = missioncontrol.QualityUnavailable
	observation.Payload = map[string]any{"status": "unavailable"}
	return observation
}

func sourceDigest(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
