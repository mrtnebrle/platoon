package missioncontrol

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
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	TypedRunSchema          = "platoon.typed-run/v1alpha1"
	observationObjectSchema = "platoon.observation/v1alpha1"
	projectionSchema        = "platoon.projection/v1alpha1"
	eventSchema             = "platoon.event/v1alpha1"
	transitionSchema        = "platoon.transition-commit/v1alpha1"
	runPointerSchema        = "platoon.run-pointer/v1alpha1"
	maxTypedObjectSize      = 4 << 20
)

var ErrTypedRunFenced = errors.New("typed run mutation is fenced")

type TypedRunStatus string

type PublicationBoundary string

const (
	TypedRunInitializing      TypedRunStatus = "initializing"
	TypedRunQuarantined       TypedRunStatus = "quarantined"
	TypedRunReconcileRequired TypedRunStatus = "reconcile_required"
)

const (
	BoundaryPacketSynced         PublicationBoundary = "packet_synced"
	BoundaryPacketPublished      PublicationBoundary = "packet_published"
	BoundaryObservationSynced    PublicationBoundary = "observation_synced"
	BoundaryObservationPublished PublicationBoundary = "observation_published"
	BoundaryProjectionSynced     PublicationBoundary = "projection_synced"
	BoundaryProjectionPublished  PublicationBoundary = "projection_published"
	BoundaryEventSynced          PublicationBoundary = "event_synced"
	BoundaryEventPublished       PublicationBoundary = "event_published"
	BoundaryTransitionSynced     PublicationBoundary = "transition_synced"
	BoundaryTransitionPublished  PublicationBoundary = "transition_published"
	BoundaryBeforePointerPublish PublicationBoundary = "before_pointer_publish"
	BoundaryAfterPointerPublish  PublicationBoundary = "after_pointer_publish"
	BoundaryAfterPointerSync     PublicationBoundary = "after_pointer_sync"
)

type RollbackEvidence struct {
	ArtifactVersion  string `json:"artifactVersion"`
	ArtifactDigest   string `json:"artifactDigest"`
	FixtureDigest    string `json:"fixtureDigest"`
	ReadableSchema   string `json:"readableSchema"`
	CreationDisabled bool   `json:"creationDisabled"`
}

type GenesisInput struct {
	RunID       string
	Packet      PacketPreview
	PublishedAt time.Time
	Rollback    RollbackEvidence
}

type TypedRunFence struct {
	RepairEpoch      uint64 `json:"repairEpoch"`
	Generation       uint64 `json:"generation"`
	TransitionDigest string `json:"transitionDigest"`
}

type TransitionReference struct {
	Generation           uint64 `json:"generation"`
	TransitionDigest     string `json:"transitionDigest"`
	ResultingStateDigest string `json:"resultingStateDigest"`
}

type TypedRunState struct {
	Schema               string           `json:"schema"`
	RunID                string           `json:"runId"`
	Status               TypedRunStatus   `json:"status"`
	EffectsEnabled       bool             `json:"effectsEnabled"`
	PacketDigest         string           `json:"packetDigest"`
	ObservationDigests   []string         `json:"observationDigests"`
	ProjectionDigest     string           `json:"projectionDigest"`
	ProjectionRevision   uint64           `json:"projectionRevision"`
	EventDigest          string           `json:"eventDigest"`
	Rollback             RollbackEvidence `json:"rollbackEvidence"`
	ResultingStateDigest string           `json:"resultingStateDigest"`
}

type TypedRunSnapshot struct {
	Fence    TypedRunFence        `json:"fence"`
	Previous *TransitionReference `json:"previous,omitempty"`
	State    TypedRunState        `json:"state"`
}

type TypedRunStore struct {
	root      string
	failpoint func(PublicationBoundary) error
}

type compiledPacket struct {
	Envelope     json.RawMessage
	Sources      []source
	Observations []SourceObservation
}

type packetObject struct {
	Schema   string          `json:"schema"`
	ID       string          `json:"id"`
	Envelope json.RawMessage `json:"envelope"`
}

type packetEnvelope struct {
	Schema                  string          `json:"schema"`
	Manifest                json.RawMessage `json:"manifest"`
	Declaration             json.RawMessage `json:"declaration"`
	ManifestDigest          string          `json:"manifestDigest"`
	DeclarationDigest       string          `json:"declarationDigest"`
	ManifestSourceDigest    string          `json:"manifestSourceDigest"`
	DeclarationSourceDigest string          `json:"declarationSourceDigest"`
	IntentRevision          string          `json:"intentRevision"`
	IntentMediaType         string          `json:"intentMediaType"`
	Handoffs                json.RawMessage `json:"handoffs"`
	Sources                 json.RawMessage `json:"sources"`
	BundleID                string          `json:"bundleId"`
	ContentSetDigest        string          `json:"contentSetDigest"`
}

type observationObject struct {
	Schema      string            `json:"schema"`
	Digest      string            `json:"digest"`
	Observation SourceObservation `json:"observation"`
}

type projectionEntry struct {
	Role              string `json:"role"`
	SourceID          string `json:"sourceId"`
	Locator           string `json:"locator"`
	Revision          string `json:"revision"`
	Reason            string `json:"reason"`
	Exposure          string `json:"exposure"`
	ObservationDigest string `json:"observationDigest"`
}

type projectionObject struct {
	Schema              string            `json:"schema"`
	Digest              string            `json:"digest"`
	RunID               string            `json:"runId"`
	PacketDigest        string            `json:"packetDigest"`
	Revision            uint64            `json:"revision"`
	PreviousRevision    *uint64           `json:"previousRevision"`
	PreviousEventDigest *string           `json:"previousEventDigest"`
	Entries             []projectionEntry `json:"entries"`
}

type eventObject struct {
	Schema              string   `json:"schema"`
	Digest              string   `json:"digest"`
	RunID               string   `json:"runId"`
	Sequence            uint64   `json:"sequence"`
	PreviousEventDigest *string  `json:"previousEventDigest"`
	RunGeneration       uint64   `json:"runGeneration"`
	ProjectionRevision  uint64   `json:"projectionRevision"`
	OccurredAt          string   `json:"occurredAt"`
	Type                string   `json:"type"`
	Subject             string   `json:"subject"`
	EvidenceDigests     []string `json:"evidenceDigests"`
}

type transitionCommit struct {
	Schema                       string               `json:"schema"`
	Digest                       string               `json:"digest"`
	RunID                        string               `json:"runId"`
	RepairEpoch                  uint64               `json:"repairEpoch"`
	Generation                   uint64               `json:"generation"`
	PreviousTransitionDigest     *string              `json:"previousTransitionDigest"`
	PreviousGeneration           *uint64              `json:"previousGeneration"`
	PreviousResultingStateDigest *string              `json:"previousResultingStateDigest"`
	PreviousEventDigest          *string              `json:"previousEventDigest"`
	PreviousProjectionRevision   *uint64              `json:"previousProjectionRevision"`
	Quarantined                  *TransitionReference `json:"quarantined,omitempty"`
	RecoveryBase                 *TransitionReference `json:"recoveryBase,omitempty"`
	Reason                       string               `json:"reason,omitempty"`
	PacketDigest                 string               `json:"packetDigest"`
	ObservationDigests           []string             `json:"observationDigests"`
	ProjectionDigest             string               `json:"projectionDigest"`
	EventDigest                  string               `json:"eventDigest"`
	ResultingState               TypedRunState        `json:"resultingState"`
}

type runPointer struct {
	Schema      string               `json:"schema"`
	RunID       string               `json:"runId"`
	RepairEpoch uint64               `json:"repairEpoch"`
	Current     TransitionReference  `json:"current"`
	Previous    *TransitionReference `json:"previous"`
}

func OpenTypedRunStore(root string) (*TypedRunStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve typed state root: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("typed state root must be a restrictive real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect typed state root: %w", err)
	}
	return &TypedRunStore{root: absolute}, nil
}

func (s *TypedRunStore) PublishGenesis(input GenesisInput) (*TypedRunSnapshot, error) {
	if s == nil || !safeOpaque(input.RunID) || input.Packet.compiled == nil || input.Packet.ID == "" ||
		input.PublishedAt.IsZero() || input.PublishedAt.Location() != time.UTC {
		return nil, errors.New("typed genesis input is invalid")
	}
	if err := validateRollbackEvidence(input.Rollback); err != nil {
		return nil, err
	}
	if len(input.Packet.compiled.Sources) == 0 || len(input.Packet.compiled.Observations) == 0 {
		return nil, errors.New("compiled packet lacks source observations")
	}
	packetID := digestCanonicalBytes(PacketSchema, input.Packet.compiled.Envelope)
	if packetID != input.Packet.ID {
		return nil, fmt.Errorf("compiled packet binding is invalid: have %s want %s", packetID, input.Packet.ID)
	}
	if _, err := validatePacketEnvelope(input.Packet.compiled.Envelope); err != nil {
		return nil, fmt.Errorf("compiled packet envelope is invalid: %w", err)
	}
	packet := packetObject{Schema: PacketSchema, ID: packetID, Envelope: bytes.Clone(input.Packet.compiled.Envelope)}

	rebuiltBundle, err := NewSourceBundle("declaration", "catalog", "operator", "entry", input.Packet.compiled.Observations)
	if err != nil || len(rebuiltBundle.Observations) != len(input.Packet.compiled.Observations) {
		return nil, errors.New("compiled observation set is invalid")
	}
	observations := make([]observationObject, 0, len(rebuiltBundle.Observations))
	observationByID := make(map[string]SourceObservation, len(rebuiltBundle.Observations))
	observationDigests := make([]string, 0, len(rebuiltBundle.Observations))
	for index, observation := range rebuiltBundle.Observations {
		supplied := input.Packet.compiled.Observations[index]
		if observation.SourceID != supplied.SourceID || observation.ContentDigest != supplied.ContentDigest ||
			observation.EnvelopeDigest != supplied.EnvelopeDigest || !validDigest(observation.EnvelopeDigest) {
			return nil, errors.New("compiled observation binding is invalid")
		}
		observations = append(observations, observationObject{Schema: observationObjectSchema, Digest: observation.EnvelopeDigest, Observation: observation})
		observationByID[observation.SourceID] = observation
		observationDigests = append(observationDigests, observation.EnvelopeDigest)
	}
	projection := projectionObject{
		Schema: projectionSchema, RunID: input.RunID, PacketDigest: packetID, Revision: 0,
		PreviousRevision: nil, PreviousEventDigest: nil,
	}
	for _, declared := range input.Packet.compiled.Sources {
		observation, ok := observationByID[declared.ID]
		if !ok || observation.Quality != QualityVerified {
			return nil, errors.New("compiled projection source is not verified")
		}
		revision := observation.Revision
		if declared.ObservationPolicy != "" {
			revision = observation.ObservedAt
		}
		projection.Entries = append(projection.Entries, projectionEntry{
			Role: declared.Role, SourceID: declared.ID, Locator: declared.Locator, Revision: revision,
			Reason: declared.Reason, Exposure: "bound", ObservationDigest: observation.EnvelopeDigest,
		})
	}
	sort.Slice(projection.Entries, func(i, j int) bool { return projection.Entries[i].SourceID < projection.Entries[j].SourceID })
	if err := validateProjectionEntries(projection.Entries, observationDigests); err != nil {
		return nil, err
	}
	projection.Digest, err = digestWithoutField(projectionSchema, projection, func(value *projectionObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	event := eventObject{
		Schema: eventSchema, RunID: input.RunID, Sequence: 1, PreviousEventDigest: nil,
		RunGeneration: 1, ProjectionRevision: 0, OccurredAt: input.PublishedAt.Format(time.RFC3339Nano),
		Type: "packet_run_published", Subject: input.RunID, EvidenceDigests: append([]string(nil), observationDigests...),
	}
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	state := TypedRunState{
		Schema: TypedRunSchema, RunID: input.RunID, Status: TypedRunInitializing, EffectsEnabled: false,
		PacketDigest: packetID, ObservationDigests: append([]string(nil), observationDigests...),
		ProjectionDigest: projection.Digest, ProjectionRevision: 0, EventDigest: event.Digest, Rollback: input.Rollback,
	}
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return nil, err
	}
	commit := transitionCommit{
		Schema: transitionSchema, RunID: input.RunID, RepairEpoch: 0, Generation: 1,
		PreviousTransitionDigest: nil, PreviousGeneration: nil, PreviousResultingStateDigest: nil,
		PreviousEventDigest: nil, PreviousProjectionRevision: nil,
		PacketDigest: packetID, ObservationDigests: append([]string(nil), observationDigests...),
		ProjectionDigest: projection.Digest, EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	pointer := runPointer{
		Schema: runPointerSchema, RunID: input.RunID, RepairEpoch: 0,
		Current:  TransitionReference{Generation: 1, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: nil,
	}
	preflight := []any{packet, projection, event, state, commit, pointer}
	for _, observation := range observations {
		preflight = append(preflight, observation)
	}
	if err := validateTypedObjectSizes(preflight...); err != nil {
		return nil, err
	}
	if existing, loadErr := s.Load(input.RunID); loadErr == nil {
		if existing.Fence.RepairEpoch == 0 && existing.Fence.Generation == 1 && existing.Fence.TransitionDigest == commit.Digest {
			if err := syncTypedDirectory(filepath.Dir(s.pointerPath(input.RunID))); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, errors.New("typed run genesis conflicts with current authority")
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		if _, statErr := os.Lstat(s.pointerPath(input.RunID)); statErr == nil {
			return nil, fmt.Errorf("typed run authority is invalid: %w", loadErr)
		}
	}

	if err := s.prepareRunDirectories(input.RunID); err != nil {
		return nil, err
	}
	if err := s.writeImmutable(input.RunID, "packets", packet.ID, packet); err != nil {
		return nil, err
	}
	for _, observation := range observations {
		if err := s.writeImmutable(input.RunID, "observations", observation.Digest, observation); err != nil {
			return nil, err
		}
	}
	if err := s.writeImmutable(input.RunID, "projections", projection.Digest, projection); err != nil {
		return nil, err
	}
	if err := s.writeImmutable(input.RunID, "events", event.Digest, event); err != nil {
		return nil, err
	}
	if err := s.writeImmutable(input.RunID, "transitions", commit.Digest, commit); err != nil {
		return nil, err
	}
	if err := createJSONFileWithHooks(s.pointerPath(input.RunID), pointer,
		func() error { return s.hit(BoundaryBeforePointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerSync) }); err != nil {
		return nil, fmt.Errorf("publish typed run pointer: %w", err)
	}
	return s.Load(input.RunID)
}

func (s *TypedRunStore) Recover(runID string, expected TypedRunFence) (*TypedRunSnapshot, error) {
	if s == nil || !safeOpaque(runID) || expected.Generation == 0 || !validDigest(expected.TransitionDigest) {
		return nil, errors.New("typed recovery input is invalid")
	}
	if err := s.validateRunDirectories(runID); err != nil {
		return nil, err
	}
	release, err := s.acquireWriterLock(runID)
	if err != nil {
		return nil, err
	}
	defer release()

	pointerRaw, err := readBoundedRegular(s.pointerPath(runID))
	if err != nil {
		return nil, err
	}
	var pointer runPointer
	if err := decodeStrictJSON(pointerRaw, &pointer); err != nil {
		return nil, errors.New("typed run pointer is malformed")
	}
	if pointer.Schema != runPointerSchema || pointer.RunID != runID || pointer.Current.Generation == 0 ||
		!validDigest(pointer.Current.TransitionDigest) || !validDigest(pointer.Current.ResultingStateDigest) {
		return nil, errors.New("typed run pointer is invalid")
	}
	if pointer.RepairEpoch != expected.RepairEpoch || pointer.Current.Generation != expected.Generation ||
		pointer.Current.TransitionDigest != expected.TransitionDigest {
		return nil, ErrTypedRunFenced
	}

	current, loadErr := s.Load(runID)
	if loadErr == nil {
		switch current.State.Status {
		case TypedRunQuarantined:
			return s.publishRepair(runID, pointerRaw, pointer)
		case TypedRunInitializing, TypedRunReconcileRequired:
			if err := syncTypedDirectory(filepath.Dir(s.pointerPath(runID))); err != nil {
				return nil, err
			}
			return current, nil
		default:
			return nil, errors.New("typed recovery state is unsupported")
		}
	}
	if pointer.Previous == nil {
		return nil, fmt.Errorf("typed current transition is invalid without a verified predecessor: %w", loadErr)
	}
	base, err := s.loadGenesisReference(runID, *pointer.Previous)
	if err != nil {
		return nil, err
	}
	if _, err := s.publishQuarantine(runID, pointerRaw, pointer, base); err != nil {
		return nil, err
	}
	quarantineRaw, err := readBoundedRegular(s.pointerPath(runID))
	if err != nil {
		return nil, err
	}
	var quarantinePointer runPointer
	if err := decodeStrictJSON(quarantineRaw, &quarantinePointer); err != nil {
		return nil, err
	}
	return s.publishRepair(runID, quarantineRaw, quarantinePointer)
}

func (s *TypedRunStore) publishQuarantine(runID string, pointerRaw []byte, pointer runPointer, base transitionCommit) (*TypedRunSnapshot, error) {
	previousEvent, err := s.loadEvent(runID, base.EventDigest)
	if err != nil {
		return nil, err
	}
	event := recoveryEvent(runID, pointer.Current.Generation+1, 2, "current_transition_quarantined", base.EventDigest, base.ObservationDigests, previousEvent.OccurredAt)
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	state := base.ResultingState
	state.Status = TypedRunQuarantined
	state.EventDigest = event.Digest
	state.ResultingStateDigest = ""
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return nil, err
	}
	invalid := pointer.Current
	recoveryBase := *pointer.Previous
	previousDigest, previousGeneration, previousState := invalid.TransitionDigest, invalid.Generation, invalid.ResultingStateDigest
	previousEventDigest, previousProjectionRevision := base.EventDigest, base.ResultingState.ProjectionRevision
	commit := transitionCommit{
		Schema: transitionSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch + 1, Generation: invalid.Generation + 1,
		PreviousTransitionDigest: &previousDigest, PreviousGeneration: &previousGeneration, PreviousResultingStateDigest: &previousState,
		PreviousEventDigest: &previousEventDigest, PreviousProjectionRevision: &previousProjectionRevision,
		Quarantined: &invalid, RecoveryBase: &recoveryBase, Reason: "current_transition_invalid",
		PacketDigest: base.PacketDigest, ObservationDigests: append([]string(nil), base.ObservationDigests...),
		ProjectionDigest: base.ProjectionDigest, EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	next := runPointer{
		Schema: runPointerSchema, RunID: runID, RepairEpoch: commit.RepairEpoch,
		Current:  TransitionReference{Generation: commit.Generation, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: &recoveryBase,
	}
	if err := s.writeRecoveryObjects(runID, event, commit); err != nil {
		return nil, err
	}
	if err := s.replacePointer(runID, pointerRaw, next); err != nil {
		return nil, err
	}
	return s.Load(runID)
}

func (s *TypedRunStore) publishRepair(runID string, pointerRaw []byte, pointer runPointer) (*TypedRunSnapshot, error) {
	if pointer.Previous == nil {
		return nil, errors.New("typed quarantine lacks a recovery base")
	}
	var quarantine transitionCommit
	if err := s.readObject(runID, "transitions", pointer.Current.TransitionDigest, &quarantine); err != nil {
		return nil, err
	}
	if err := s.validateQuarantineCommit(runID, pointer, quarantine); err != nil {
		return nil, err
	}
	previousEvent, err := s.loadEvent(runID, quarantine.EventDigest)
	if err != nil {
		return nil, err
	}
	event := recoveryEvent(runID, pointer.Current.Generation+1, 3, "reconcile_required", quarantine.EventDigest, quarantine.ObservationDigests, previousEvent.OccurredAt)
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	state := quarantine.ResultingState
	state.Status = TypedRunReconcileRequired
	state.EventDigest = event.Digest
	state.ResultingStateDigest = ""
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return nil, err
	}
	previousDigest, previousGeneration, previousState := pointer.Current.TransitionDigest, pointer.Current.Generation, pointer.Current.ResultingStateDigest
	previousEventDigest, previousProjectionRevision := quarantine.EventDigest, quarantine.ResultingState.ProjectionRevision
	recoveryBase := *pointer.Previous
	commit := transitionCommit{
		Schema: transitionSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch, Generation: pointer.Current.Generation + 1,
		PreviousTransitionDigest: &previousDigest, PreviousGeneration: &previousGeneration, PreviousResultingStateDigest: &previousState,
		PreviousEventDigest: &previousEventDigest, PreviousProjectionRevision: &previousProjectionRevision,
		RecoveryBase: &recoveryBase, PacketDigest: quarantine.PacketDigest,
		ObservationDigests: append([]string(nil), quarantine.ObservationDigests...), ProjectionDigest: quarantine.ProjectionDigest,
		EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	quarantineReference := pointer.Current
	next := runPointer{
		Schema: runPointerSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch,
		Current:  TransitionReference{Generation: commit.Generation, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: &quarantineReference,
	}
	if err := s.writeRecoveryObjects(runID, event, commit); err != nil {
		return nil, err
	}
	if err := s.replacePointer(runID, pointerRaw, next); err != nil {
		return nil, err
	}
	return s.Load(runID)
}

func recoveryEvent(runID string, generation, sequence uint64, eventType, previousDigest string, observations []string, previousTime string) eventObject {
	occurredAt, _ := time.Parse(time.RFC3339Nano, previousTime)
	occurredAt = occurredAt.Add(time.Nanosecond)
	return eventObject{
		Schema: eventSchema, RunID: runID, Sequence: sequence, PreviousEventDigest: &previousDigest,
		RunGeneration: generation, ProjectionRevision: 0, OccurredAt: occurredAt.Format(time.RFC3339Nano),
		Type: eventType, Subject: runID, EvidenceDigests: append([]string(nil), observations...),
	}
}

func (s *TypedRunStore) writeRecoveryObjects(runID string, event eventObject, commit transitionCommit) error {
	if err := s.writeImmutable(runID, "events", event.Digest, event); err != nil {
		return err
	}
	if err := s.writeImmutable(runID, "transitions", commit.Digest, commit); err != nil {
		return err
	}
	return nil
}

func (s *TypedRunStore) replacePointer(runID string, expectedRaw []byte, pointer runPointer) error {
	return replaceJSONFileWithHooks(s.pointerPath(runID), expectedRaw, pointer,
		func() error { return s.hit(BoundaryBeforePointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerSync) })
}

func (s *TypedRunStore) loadEvent(runID, digest string) (eventObject, error) {
	var event eventObject
	if err := s.readObject(runID, "events", digest, &event); err != nil {
		return eventObject{}, err
	}
	return event, nil
}

func (s *TypedRunStore) Load(runID string) (*TypedRunSnapshot, error) {
	if s == nil || !safeOpaque(runID) {
		return nil, errors.New("typed run id is invalid")
	}
	if err := s.validateRunDirectories(runID); err != nil {
		return nil, err
	}
	var pointer runPointer
	if err := readStrictJSON(s.pointerPath(runID), &pointer); err != nil {
		return nil, err
	}
	if pointer.Schema != runPointerSchema || pointer.RunID != runID || pointer.Current.Generation == 0 ||
		!validDigest(pointer.Current.TransitionDigest) || !validDigest(pointer.Current.ResultingStateDigest) {
		return nil, errors.New("typed run pointer is invalid")
	}
	if pointer.Previous != nil && (pointer.Previous.Generation == 0 || pointer.Previous.Generation >= pointer.Current.Generation ||
		!validDigest(pointer.Previous.TransitionDigest) || !validDigest(pointer.Previous.ResultingStateDigest)) {
		return nil, errors.New("typed run pointer predecessor is invalid")
	}
	var commit transitionCommit
	if err := s.readObject(runID, "transitions", pointer.Current.TransitionDigest, &commit); err != nil {
		return nil, fmt.Errorf("load current transition: %w", err)
	}
	switch commit.ResultingState.Status {
	case TypedRunInitializing:
		if err := s.validateGenesisCommit(runID, pointer, commit); err != nil {
			return nil, err
		}
	case TypedRunQuarantined:
		if err := s.validateQuarantineCommit(runID, pointer, commit); err != nil {
			return nil, err
		}
	case TypedRunReconcileRequired:
		if err := s.validateRepairCommit(runID, pointer, commit); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("typed run status is unsupported")
	}
	return &TypedRunSnapshot{
		Fence:    TypedRunFence{RepairEpoch: pointer.RepairEpoch, Generation: pointer.Current.Generation, TransitionDigest: pointer.Current.TransitionDigest},
		Previous: pointer.Previous, State: commit.ResultingState,
	}, nil
}

func (s *TypedRunStore) validateGenesisCommit(runID string, pointer runPointer, commit transitionCommit) error {
	if commit.Schema != transitionSchema || commit.RunID != runID || commit.Generation != 1 || commit.RepairEpoch != 0 ||
		commit.PreviousTransitionDigest != nil || commit.PreviousGeneration != nil || commit.PreviousResultingStateDigest != nil ||
		commit.PreviousEventDigest != nil || commit.PreviousProjectionRevision != nil ||
		commit.Quarantined != nil || commit.RecoveryBase != nil || commit.Reason != "" || pointer.Previous != nil || pointer.RepairEpoch != 0 ||
		commit.Digest != pointer.Current.TransitionDigest || commit.Generation != pointer.Current.Generation {
		return errors.New("typed genesis transition binding is invalid")
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("typed genesis transition digest is invalid")
	}
	if commit.ResultingState.Schema != TypedRunSchema || commit.ResultingState.RunID != runID ||
		commit.ResultingState.Status != TypedRunInitializing || commit.ResultingState.EffectsEnabled ||
		commit.ResultingState.PacketDigest != commit.PacketDigest || commit.ResultingState.ProjectionDigest != commit.ProjectionDigest ||
		commit.ResultingState.ProjectionRevision != 0 || commit.ResultingState.EventDigest != commit.EventDigest ||
		!sameStrings(commit.ResultingState.ObservationDigests, commit.ObservationDigests) {
		return errors.New("typed genesis resulting state is invalid")
	}
	wantState, err := digestState(commit.ResultingState)
	if err != nil || wantState != commit.ResultingState.ResultingStateDigest || wantState != pointer.Current.ResultingStateDigest {
		return errors.New("typed genesis resulting state digest is invalid")
	}
	return s.validateCommitObjects(runID, commit, eventExpectation{Sequence: 1, Type: "packet_run_published"})
}

type eventExpectation struct {
	Sequence       uint64
	Type           string
	PreviousDigest *string
}

func (s *TypedRunStore) validateCommitObjects(runID string, commit transitionCommit, expected eventExpectation) error {
	if err := validateRollbackEvidence(commit.ResultingState.Rollback); err != nil {
		return err
	}
	if !validUniqueDigests(commit.ObservationDigests) {
		return errors.New("typed transition observation set is invalid")
	}
	var packet packetObject
	if err := s.readObject(runID, "packets", commit.PacketDigest, &packet); err != nil {
		return fmt.Errorf("load packet object: %w", err)
	}
	if packet.Schema != PacketSchema || packet.ID != commit.PacketDigest {
		return errors.New("typed packet object binding is invalid")
	}
	wantPacket := digestCanonicalBytes(PacketSchema, packet.Envelope)
	envelope, err := validatePacketEnvelope(packet.Envelope)
	if err != nil || wantPacket != packet.ID {
		return errors.New("typed packet digest is invalid")
	}
	contentSet := make([]any, 0, len(commit.ObservationDigests))
	lastSourceID := ""
	for _, digest := range commit.ObservationDigests {
		var object observationObject
		if err := s.readObject(runID, "observations", digest, &object); err != nil {
			return fmt.Errorf("load observation object: %w", err)
		}
		if object.Schema != observationObjectSchema || object.Digest != digest {
			return errors.New("typed observation object binding is invalid")
		}
		rebuilt, err := NewSourceBundle("declaration", "catalog", "operator", "entry", []SourceObservation{object.Observation})
		if err != nil || rebuilt.Observations[0].ContentDigest != object.Observation.ContentDigest ||
			rebuilt.Observations[0].EnvelopeDigest != object.Observation.EnvelopeDigest || object.Observation.EnvelopeDigest != digest {
			return errors.New("typed observation object digest is invalid")
		}
		if lastSourceID != "" && lastSourceID >= object.Observation.SourceID {
			return errors.New("typed observation set order is invalid")
		}
		lastSourceID = object.Observation.SourceID
		contentSet = append(contentSet, map[string]any{"contentDigest": object.Observation.ContentDigest, "sourceId": object.Observation.SourceID})
	}
	wantContentSet, err := canonicalDigest("platoon.source-content-set/v1alpha1", contentSet)
	if err != nil || wantContentSet != envelope.ContentSetDigest {
		return errors.New("typed packet observation set binding is invalid")
	}
	var projection projectionObject
	if err := s.readObject(runID, "projections", commit.ProjectionDigest, &projection); err != nil {
		return fmt.Errorf("load projection object: %w", err)
	}
	wantProjection, err := digestWithoutField(projectionSchema, projection, func(value *projectionObject) { value.Digest = "" })
	if err != nil || projection.Schema != projectionSchema || projection.RunID != runID || projection.Revision != 0 ||
		projection.PacketDigest != commit.PacketDigest || projection.PreviousRevision != nil || projection.PreviousEventDigest != nil ||
		projection.Digest != commit.ProjectionDigest || wantProjection != projection.Digest || projection.Revision != commit.ResultingState.ProjectionRevision {
		return errors.New("typed projection object is invalid")
	}
	if err := validateProjectionEntries(projection.Entries, commit.ObservationDigests); err != nil {
		return err
	}
	var event eventObject
	if err := s.readObject(runID, "events", commit.EventDigest, &event); err != nil {
		return fmt.Errorf("load event object: %w", err)
	}
	wantEvent, err := digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if _, timeErr := time.Parse(time.RFC3339Nano, event.OccurredAt); timeErr != nil {
		return errors.New("typed event time is invalid")
	}
	if err != nil || event.Schema != eventSchema || event.RunID != runID || event.Sequence != expected.Sequence || event.RunGeneration != commit.Generation ||
		event.ProjectionRevision != 0 || !sameOptionalString(event.PreviousEventDigest, expected.PreviousDigest) || event.Type != expected.Type || event.Subject != runID ||
		event.Digest != commit.EventDigest || wantEvent != event.Digest || !sameStrings(event.EvidenceDigests, commit.ObservationDigests) {
		return errors.New("typed event object is invalid")
	}
	return nil
}

func (s *TypedRunStore) validateQuarantineCommit(runID string, pointer runPointer, commit transitionCommit) error {
	if pointer.Previous == nil || commit.Quarantined == nil || commit.RecoveryBase == nil || commit.Reason != "current_transition_invalid" ||
		commit.Schema != transitionSchema || commit.RunID != runID || pointer.RepairEpoch == 0 || commit.RepairEpoch != pointer.RepairEpoch ||
		commit.Generation != pointer.Current.Generation || commit.Digest != pointer.Current.TransitionDigest ||
		commit.PreviousTransitionDigest == nil || *commit.PreviousTransitionDigest != commit.Quarantined.TransitionDigest ||
		commit.PreviousGeneration == nil || *commit.PreviousGeneration != commit.Quarantined.Generation ||
		commit.PreviousResultingStateDigest == nil || *commit.PreviousResultingStateDigest != commit.Quarantined.ResultingStateDigest ||
		commit.PreviousEventDigest == nil || commit.PreviousProjectionRevision == nil ||
		*commit.RecoveryBase != *pointer.Previous || commit.Quarantined.Generation+1 != commit.Generation {
		return errors.New("typed quarantine transition binding is invalid")
	}
	if err := s.validateTransitionState(pointer, commit, TypedRunQuarantined); err != nil {
		return err
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("typed quarantine transition digest is invalid")
	}
	base, err := s.loadGenesisReference(runID, *commit.RecoveryBase)
	if err != nil {
		return err
	}
	if *commit.PreviousEventDigest != base.EventDigest || *commit.PreviousProjectionRevision != base.ResultingState.ProjectionRevision {
		return errors.New("typed quarantine recovery base is inconsistent")
	}
	previousEvent := base.ResultingState.EventDigest
	return s.validateCommitObjects(runID, commit, eventExpectation{Sequence: 2, Type: "current_transition_quarantined", PreviousDigest: &previousEvent})
}

func (s *TypedRunStore) validateRepairCommit(runID string, pointer runPointer, commit transitionCommit) error {
	if pointer.Previous == nil || commit.RecoveryBase == nil || commit.Quarantined != nil || commit.Reason != "" ||
		commit.Schema != transitionSchema || commit.RunID != runID || commit.RepairEpoch != pointer.RepairEpoch ||
		commit.Generation != pointer.Current.Generation || commit.Digest != pointer.Current.TransitionDigest ||
		commit.PreviousTransitionDigest == nil || *commit.PreviousTransitionDigest != pointer.Previous.TransitionDigest ||
		commit.PreviousGeneration == nil || *commit.PreviousGeneration != pointer.Previous.Generation ||
		commit.PreviousResultingStateDigest == nil || *commit.PreviousResultingStateDigest != pointer.Previous.ResultingStateDigest ||
		commit.PreviousEventDigest == nil || commit.PreviousProjectionRevision == nil ||
		pointer.Previous.Generation+1 != commit.Generation {
		return errors.New("typed repair transition binding is invalid")
	}
	if err := s.validateTransitionState(pointer, commit, TypedRunReconcileRequired); err != nil {
		return err
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("typed repair transition digest is invalid")
	}
	var quarantine transitionCommit
	if err := s.readObject(runID, "transitions", pointer.Previous.TransitionDigest, &quarantine); err != nil {
		return fmt.Errorf("load quarantine predecessor: %w", err)
	}
	quarantinePointer := runPointer{
		Schema: runPointerSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch,
		Current: *pointer.Previous, Previous: commit.RecoveryBase,
	}
	if err := s.validateQuarantineCommit(runID, quarantinePointer, quarantine); err != nil {
		return err
	}
	if *commit.PreviousEventDigest != quarantine.EventDigest || *commit.PreviousProjectionRevision != quarantine.ResultingState.ProjectionRevision {
		return errors.New("typed repair predecessor is inconsistent")
	}
	previousEvent := quarantine.ResultingState.EventDigest
	return s.validateCommitObjects(runID, commit, eventExpectation{Sequence: 3, Type: "reconcile_required", PreviousDigest: &previousEvent})
}

func (s *TypedRunStore) validateTransitionState(pointer runPointer, commit transitionCommit, status TypedRunStatus) error {
	state := commit.ResultingState
	if state.Schema != TypedRunSchema || state.RunID != commit.RunID || state.Status != status || state.EffectsEnabled ||
		state.PacketDigest != commit.PacketDigest || state.ProjectionDigest != commit.ProjectionDigest || state.EventDigest != commit.EventDigest ||
		state.ProjectionRevision != 0 || !sameStrings(state.ObservationDigests, commit.ObservationDigests) {
		return errors.New("typed recovery resulting state is invalid")
	}
	wantState, err := digestState(state)
	if err != nil || wantState != state.ResultingStateDigest || wantState != pointer.Current.ResultingStateDigest {
		return errors.New("typed recovery resulting state digest is invalid")
	}
	return nil
}

func (s *TypedRunStore) loadGenesisReference(runID string, reference TransitionReference) (transitionCommit, error) {
	var commit transitionCommit
	if err := s.readObject(runID, "transitions", reference.TransitionDigest, &commit); err != nil {
		return transitionCommit{}, fmt.Errorf("load verified recovery base: %w", err)
	}
	pointer := runPointer{Schema: runPointerSchema, RunID: runID, Current: reference}
	if err := s.validateGenesisCommit(runID, pointer, commit); err != nil {
		return transitionCommit{}, fmt.Errorf("validate recovery base: %w", err)
	}
	return commit, nil
}

func validateRollbackEvidence(evidence RollbackEvidence) error {
	if !safeOpaque(evidence.ArtifactVersion) || !validDigest(evidence.ArtifactDigest) || !validDigest(evidence.FixtureDigest) ||
		evidence.ReadableSchema != TypedRunSchema || !evidence.CreationDisabled {
		return errors.New("typed publication requires verified creation-disabled rollback evidence")
	}
	return nil
}

func validateProjectionEntries(entries []projectionEntry, observations []string) error {
	allowed := make(map[string]bool, len(observations))
	for _, digest := range observations {
		allowed[digest] = true
	}
	seenSources := make(map[string]bool, len(entries))
	seenObservations := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !validSlug(entry.Role) || !validSlug(entry.SourceID) || seenSources[entry.SourceID] || !safeOpaque(entry.Locator) ||
			!safeOpaque(entry.Revision) || secretLike(entry.Locator) || secretLike(entry.Revision) || secretLike(entry.Reason) ||
			entry.Reason == "" || len(entry.Reason) > 512 || hasControl(entry.Reason) || entry.Exposure != "bound" || !allowed[entry.ObservationDigest] {
			return errors.New("typed projection entry is unsafe or unbound")
		}
		seenSources[entry.SourceID] = true
		seenObservations[entry.ObservationDigest] = true
	}
	if len(seenSources) != len(allowed) || len(seenObservations) != len(allowed) {
		return errors.New("typed projection observation set is incomplete")
	}
	return nil
}

func validateTypedObjectSizes(values ...any) error {
	for _, value := range values {
		raw, err := canonicalJSON(value)
		if err != nil {
			return err
		}
		if len(raw) > maxTypedObjectSize {
			return errors.New("typed state object exceeds its size limit")
		}
	}
	return nil
}

func validatePacketEnvelope(raw []byte) (packetEnvelope, error) {
	var envelope packetEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return packetEnvelope{}, err
	}
	if envelope.Schema != PacketSchema || !validDigest(envelope.ManifestDigest) || !validDigest(envelope.DeclarationDigest) ||
		!validDigest(envelope.ManifestSourceDigest) || !validDigest(envelope.DeclarationSourceDigest) || !validDigest(envelope.IntentRevision) ||
		!validDigest(envelope.BundleID) || !validDigest(envelope.ContentSetDigest) ||
		(envelope.IntentMediaType != "text/markdown" && envelope.IntentMediaType != "application/octet-stream") ||
		len(envelope.Manifest) == 0 || bytes.Equal(envelope.Manifest, []byte("null")) ||
		len(envelope.Declaration) == 0 || bytes.Equal(envelope.Declaration, []byte("null")) ||
		len(envelope.Handoffs) == 0 || bytes.Equal(envelope.Handoffs, []byte("null")) ||
		len(envelope.Sources) == 0 || bytes.Equal(envelope.Sources, []byte("null")) {
		return packetEnvelope{}, errors.New("packet fields are invalid")
	}
	value, err := parseStrictJSON(raw)
	if err != nil {
		return packetEnvelope{}, err
	}
	if err := validateDurableValue(value); err != nil {
		return packetEnvelope{}, err
	}
	if _, err := parseStrictJSON(envelope.Manifest); err != nil {
		return packetEnvelope{}, errors.New("packet manifest is invalid")
	}
	wantManifest := digestCanonicalBytes("platoon.normalized-manifest/v1alpha1", envelope.Manifest)
	if wantManifest != envelope.ManifestDigest {
		return packetEnvelope{}, errors.New("packet manifest digest is invalid")
	}
	declarationValue, err := parseStrictJSON(envelope.Declaration)
	if err != nil {
		return packetEnvelope{}, errors.New("packet declaration is invalid")
	}
	wantDeclaration := digestCanonicalBytes("platoon.normalized-declaration/v1alpha1", envelope.Declaration)
	if wantDeclaration != envelope.DeclarationDigest {
		return packetEnvelope{}, errors.New("packet declaration digest is invalid")
	}
	declaration, ok := declarationValue.(map[string]any)
	if !ok {
		return packetEnvelope{}, errors.New("packet declaration shape is invalid")
	}
	spec, ok := declaration["spec"].(map[string]any)
	if !ok || !rawValueMatches(envelope.Handoffs, spec["handoffs"]) || !rawValueMatches(envelope.Sources, spec["sources"]) {
		return packetEnvelope{}, errors.New("packet declaration references are inconsistent")
	}
	return envelope, nil
}

func rawValueMatches(raw []byte, value any) bool {
	left, err := canonicalJSON(value)
	if err != nil {
		return false
	}
	parsed, err := parseStrictJSON(raw)
	if err != nil {
		return false
	}
	right, err := canonicalJSON(parsed)
	return err == nil && bytes.Equal(left, right)
}

func validateDurableValue(value any) error {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if key == "" || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") ||
				strings.Contains(lower, "token") || lower == "raw" || strings.Contains(lower, "rawbody") || lower == "stdout" || lower == "stderr" ||
				strings.Contains(lower, "transcript") || strings.Contains(lower, "prompt") {
				return errors.New("prohibited field")
			}
			if err := validateDurableValue(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range current {
			if err := validateDurableValue(child); err != nil {
				return err
			}
		}
	case string:
		if len(current) > maxTypedObjectSize || hasControl(current) || strings.HasPrefix(current, "/") || strings.Contains(current, `\`) || secretLike(current) ||
			windowsAbsolutePath.MatchString(current) {
			return errors.New("private or invalid value")
		}
	}
	return nil
}

func digestState(state TypedRunState) (string, error) {
	state.ResultingStateDigest = ""
	return canonicalDigest(TypedRunSchema, state)
}

func digestWithoutField[T any](domain string, value T, clear func(*T)) (string, error) {
	clear(&value)
	return canonicalDigest(domain, value)
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func digestCanonicalBytes(domain string, raw []byte) string {
	digest := sha256.New()
	digest.Write([]byte(domain))
	digest.Write([]byte{0})
	digest.Write(raw)
	return hex.EncodeToString(digest.Sum(nil))
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validUniqueDigests(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !validDigest(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *TypedRunStore) prepareRunDirectories(runID string) error {
	if err := ensureSecureDirectory(s.root, true); err != nil {
		return err
	}
	for _, path := range []string{
		filepath.Join(s.root, "typed-runs"), s.runDir(runID), filepath.Join(s.runDir(runID), "objects"),
		filepath.Join(s.runDir(runID), "objects", "packets"), filepath.Join(s.runDir(runID), "objects", "observations"),
		filepath.Join(s.runDir(runID), "objects", "projections"), filepath.Join(s.runDir(runID), "objects", "events"),
		filepath.Join(s.runDir(runID), "objects", "transitions"),
	} {
		if err := ensureSecureDirectory(path, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *TypedRunStore) validateRunDirectories(runID string) error {
	for _, path := range []string{
		s.root, filepath.Join(s.root, "typed-runs"), s.runDir(runID), filepath.Join(s.runDir(runID), "objects"),
		filepath.Join(s.runDir(runID), "objects", "packets"), filepath.Join(s.runDir(runID), "objects", "observations"),
		filepath.Join(s.runDir(runID), "objects", "projections"), filepath.Join(s.runDir(runID), "objects", "events"),
		filepath.Join(s.runDir(runID), "objects", "transitions"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("typed state directory is not restrictive")
		}
	}
	return nil
}

func ensureSecureDirectory(path string, parents bool) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("typed state directory is not restrictive")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var err error
	if parents {
		err = os.MkdirAll(path, 0o700)
	} else {
		err = os.Mkdir(path, 0o700)
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return syncTypedDirectory(filepath.Dir(path))
}

func (s *TypedRunStore) writeImmutable(runID, kind, digest string, value any) error {
	path := filepath.Join(s.runDir(runID), "objects", kind, digest+".json")
	published, synced, err := objectBoundaries(kind)
	if err != nil {
		return err
	}
	return createJSONFileWithHooks(path, value, nil,
		func() error { return s.hit(published) },
		func() error { return s.hit(synced) })
}

func objectBoundaries(kind string) (PublicationBoundary, PublicationBoundary, error) {
	switch kind {
	case "packets":
		return BoundaryPacketPublished, BoundaryPacketSynced, nil
	case "observations":
		return BoundaryObservationPublished, BoundaryObservationSynced, nil
	case "projections":
		return BoundaryProjectionPublished, BoundaryProjectionSynced, nil
	case "events":
		return BoundaryEventPublished, BoundaryEventSynced, nil
	case "transitions":
		return BoundaryTransitionPublished, BoundaryTransitionSynced, nil
	default:
		return "", "", errors.New("typed object kind is invalid")
	}
}

func createJSONFileWithHooks(path string, value any, beforePublish, afterPublish, afterSync func() error) error {
	raw, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	if len(raw) > maxTypedObjectSize {
		return errors.New("typed state object exceeds its size limit")
	}
	if existing, err := readBoundedRegular(path); err == nil {
		if bytes.Equal(existing, raw) {
			if err := syncTypedDirectory(filepath.Dir(path)); err != nil {
				return err
			}
			if afterSync != nil {
				return afterSync()
			}
			return nil
		}
		return errors.New("immutable typed state object conflicts with existing bytes")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	suffixBytes := make([]byte, 8)
	if _, err := rand.Read(suffixBytes); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp."+hex.EncodeToString(suffixBytes))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return err
		}
	}
	if err := os.Link(temporary, path); err != nil {
		if existing, readErr := readBoundedRegular(path); readErr == nil && bytes.Equal(existing, raw) {
			_ = os.Remove(temporary)
			cleanup = false
			if err := syncTypedDirectory(filepath.Dir(path)); err != nil {
				return err
			}
			if afterSync != nil {
				return afterSync()
			}
			return nil
		}
		return err
	}
	if afterPublish != nil {
		if err := afterPublish(); err != nil {
			return err
		}
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	cleanup = false
	if err := syncTypedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if afterSync != nil {
		return afterSync()
	}
	return nil
}

func (s *TypedRunStore) hit(boundary PublicationBoundary) error {
	if s.failpoint == nil {
		return nil
	}
	return s.failpoint(boundary)
}

func readStrictJSON(path string, destination any) error {
	raw, err := readBoundedRegular(path)
	if err != nil {
		return err
	}
	return decodeStrictJSON(raw, destination)
}

func decodeStrictJSON(raw []byte, destination any) error {
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
		return errors.New("typed state object contains trailing JSON")
	}
	return nil
}

func replaceJSONFileWithHooks(path string, expectedRaw []byte, value any, beforePublish, afterPublish, afterSync func() error) error {
	raw, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	if len(raw) > maxTypedObjectSize {
		return errors.New("typed run pointer exceeds its size limit")
	}
	suffixBytes := make([]byte, 8)
	if _, err := rand.Read(suffixBytes); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp."+hex.EncodeToString(suffixBytes))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return err
		}
	}
	currentRaw, err := readBoundedRegular(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentRaw, expectedRaw) {
		return ErrTypedRunFenced
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	cleanup = false
	if afterPublish != nil {
		if err := afterPublish(); err != nil {
			return err
		}
	}
	if err := syncTypedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if afterSync != nil {
		return afterSync()
	}
	return nil
}

func (s *TypedRunStore) acquireWriterLock(runID string) (func(), error) {
	path := filepath.Join(s.runDir(runID), ".writer.lock")
	var before os.FileInfo
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("typed writer lock is not restrictive")
		}
		before = info
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || (before != nil && !os.SameFile(before, info)) {
		_ = file.Close()
		return nil, errors.New("typed writer lock is not restrictive")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrTypedRunFenced
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func readBoundedRegular(path string) ([]byte, error) {
	first, err := readBoundedRegularOnce(path)
	if err != nil {
		return nil, err
	}
	second, err := readBoundedRegularOnce(path)
	if err != nil || !bytes.Equal(first, second) {
		return nil, errors.New("typed state object changed while reading")
	}
	return first, nil
}

func readBoundedRegularOnce(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxTypedObjectSize {
		return nil, errors.New("typed state object is not a bounded restrictive regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("typed state object changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxTypedObjectSize+1))
	if err != nil || len(raw) > maxTypedObjectSize {
		return nil, errors.New("typed state object exceeded its size limit")
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("typed state object changed while reading")
	}
	return raw, nil
}

func (s *TypedRunStore) readObject(runID, kind, digest string, destination any) error {
	if !validDigest(digest) {
		return errors.New("typed object digest is invalid")
	}
	return readStrictJSON(filepath.Join(s.runDir(runID), "objects", kind, digest+".json"), destination)
}

func syncTypedDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (s *TypedRunStore) runDir(runID string) string {
	return filepath.Join(s.root, "typed-runs", runID)
}

func (s *TypedRunStore) pointerPath(runID string) string {
	return filepath.Join(s.runDir(runID), "current.json")
}
