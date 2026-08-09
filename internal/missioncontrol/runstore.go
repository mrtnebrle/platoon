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
	"strings"
	"syscall"
	"time"

	"github.com/mrtnebrle/platoon/internal/opaqueid"
)

const (
	TypedRunSchema           = "platoon.typed-run/v1alpha1"
	observationObjectSchema  = "platoon.observation/v1alpha1"
	projectionSchema         = "platoon.projection/v1alpha1"
	eventSchema              = "platoon.event/v1alpha1"
	transitionSchema         = "platoon.transition-commit/v1alpha1"
	runPointerSchema         = "platoon.run-pointer/v1alpha1"
	maxTypedObjectSize       = 4 << 20
	maxObservationObjectSize = 1 << 20
	maxEventObjectSize       = 64 << 10
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

type RollbackVerifier interface {
	VerifyRollback(RollbackEvidence) error
}

type SourceBundleBinding struct {
	BundleID            string `json:"bundleId"`
	DeclarationDigest   string `json:"declarationDigest"`
	SourceCatalogDigest string `json:"sourceCatalogDigest"`
	CallerRole          string `json:"callerRole"`
	QueryScope          string `json:"queryScope"`
	ContentSetDigest    string `json:"contentSetDigest"`
}

type GenesisInput struct {
	RunID       string
	Packet      PacketPreview
	PublishedAt time.Time
	Rollback    RollbackEvidence
}

type SuccessorInput struct {
	RunID       string
	Expected    TypedRunFence
	PublishedAt time.Time
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
	Schema               string              `json:"schema"`
	RunID                string              `json:"runId"`
	Status               TypedRunStatus      `json:"status"`
	EffectsEnabled       bool                `json:"effectsEnabled"`
	PacketDigest         string              `json:"packetDigest"`
	ObservationDigests   []string            `json:"observationDigests"`
	ProjectionDigest     string              `json:"projectionDigest"`
	ProjectionRevision   uint64              `json:"projectionRevision"`
	EventDigest          string              `json:"eventDigest"`
	Rollback             RollbackEvidence    `json:"rollbackEvidence"`
	SourceBundle         SourceBundleBinding `json:"sourceBundle"`
	ResultingStateDigest string              `json:"resultingStateDigest"`
}

type TypedRunSnapshot struct {
	Fence    TypedRunFence        `json:"fence"`
	Previous *TransitionReference `json:"previous,omitempty"`
	State    TypedRunState        `json:"state"`
}

type TypedRunStore struct {
	root        string
	rollback    RollbackVerifier
	failpoint   func(PublicationBoundary) error
	pointerSync func(string) error
}

type transitionCandidate struct {
	event   eventObject
	commit  transitionCommit
	pointer runPointer
}

type recoveryChain struct {
	quarantine transitionCandidate
	repair     transitionCandidate
}

type compiledPacket struct {
	Envelope json.RawMessage
	Bundle   SourceBundle
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
	SourceLabel       string `json:"sourceLabel"`
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

func OpenTypedRunStore(root string, rollback RollbackVerifier) (*TypedRunStore, error) {
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
	return &TypedRunStore{root: absolute, rollback: rollback}, nil
}

func (s *TypedRunStore) PublishGenesis(input GenesisInput) (*TypedRunSnapshot, error) {
	if s == nil || !opaqueid.Valid(input.RunID) || input.Packet.compiled == nil || input.Packet.ID == "" {
		return nil, errors.New("typed genesis input is invalid")
	}
	publishedAt, err := formatEventTime(input.PublishedAt)
	if err != nil {
		return nil, errors.New("typed genesis input time is invalid")
	}
	if err := s.validateRollbackEvidence(input.Rollback); err != nil {
		return nil, err
	}
	if len(input.Packet.compiled.Bundle.Observations) == 0 {
		return nil, errors.New("compiled packet lacks source observations")
	}
	packetID := digestCanonicalBytes(PacketSchema, input.Packet.compiled.Envelope)
	if packetID != input.Packet.ID {
		return nil, fmt.Errorf("compiled packet binding is invalid: have %s want %s", packetID, input.Packet.ID)
	}
	envelope, err := validatePacketEnvelope(input.Packet.compiled.Envelope)
	if err != nil {
		return nil, fmt.Errorf("compiled packet envelope is invalid: %w", err)
	}
	sources, err := packetSources(envelope)
	if err != nil {
		return nil, err
	}
	packet := packetObject{Schema: PacketSchema, ID: packetID, Envelope: bytes.Clone(input.Packet.compiled.Envelope)}

	suppliedBundle := input.Packet.compiled.Bundle
	rebuiltBundle, err := NewSourceBundle(suppliedBundle.DeclarationDigest, suppliedBundle.SourceCatalogDigest, suppliedBundle.CallerRole, suppliedBundle.QueryScope, suppliedBundle.Observations)
	if err != nil || len(rebuiltBundle.Observations) != len(suppliedBundle.Observations) || rebuiltBundle.BundleID != suppliedBundle.BundleID ||
		rebuiltBundle.ContentSetDigest != suppliedBundle.ContentSetDigest || rebuiltBundle.BundleID != envelope.BundleID ||
		rebuiltBundle.ContentSetDigest != envelope.ContentSetDigest || rebuiltBundle.DeclarationDigest != envelope.DeclarationDigest {
		return nil, errors.New("compiled observation set is invalid")
	}
	wantCatalog, err := sourceCatalogDigest(sources)
	if err != nil || rebuiltBundle.SourceCatalogDigest != wantCatalog {
		return nil, errors.New("compiled source catalog binding is invalid")
	}
	observations := make([]observationObject, 0, len(rebuiltBundle.Observations))
	observationDigests := make([]string, 0, len(rebuiltBundle.Observations))
	for index, observation := range rebuiltBundle.Observations {
		supplied := suppliedBundle.Observations[index]
		if observation.SourceID != supplied.SourceID || observation.ContentDigest != supplied.ContentDigest ||
			observation.EnvelopeDigest != supplied.EnvelopeDigest || !validDigest(observation.EnvelopeDigest) {
			return nil, errors.New("compiled observation binding is invalid")
		}
		observations = append(observations, observationObject{Schema: observationObjectSchema, Digest: observation.EnvelopeDigest, Observation: observation})
		observationDigests = append(observationDigests, observation.EnvelopeDigest)
	}
	projection := projectionObject{
		Schema: projectionSchema, RunID: input.RunID, PacketDigest: packetID, Revision: 0,
		PreviousRevision: nil, PreviousEventDigest: nil,
	}
	projection.Entries, err = buildProjectionEntries(sources, rebuiltBundle.Observations)
	if err != nil {
		return nil, err
	}
	if err := validateProjectionEntries(projection.Entries, observationDigests); err != nil {
		return nil, err
	}
	projection.Digest, err = digestWithoutField(projectionSchema, projection, func(value *projectionObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	event := eventObject{
		Schema: eventSchema, RunID: input.RunID, Sequence: 1, PreviousEventDigest: nil,
		RunGeneration: 1, ProjectionRevision: 0, OccurredAt: publishedAt,
		Type: "packet_run_published", Subject: input.RunID, EvidenceDigests: append([]string(nil), observationDigests...),
	}
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	if err := validateEventSemantics(event); err != nil {
		return nil, err
	}
	state := TypedRunState{
		Schema: TypedRunSchema, RunID: input.RunID, Status: TypedRunInitializing, EffectsEnabled: false,
		PacketDigest: packetID, ObservationDigests: append([]string(nil), observationDigests...),
		ProjectionDigest: projection.Digest, ProjectionRevision: 0, EventDigest: event.Digest, Rollback: input.Rollback,
		SourceBundle: sourceBundleBinding(rebuiltBundle),
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
	preflight := []struct {
		kind  string
		value any
	}{{"packets", packet}, {"projections", projection}, {"events", event}, {"transitions", commit}, {"pointer", pointer}}
	for _, observation := range observations {
		preflight = append(preflight, struct {
			kind  string
			value any
		}{"observations", observation})
	}
	for _, candidate := range preflight {
		if err := validateTypedObjectSize(candidate.kind, candidate.value); err != nil {
			return nil, err
		}
	}
	if existing, loadErr := s.Load(input.RunID); loadErr == nil {
		if existing.Fence.RepairEpoch == 0 && existing.Fence.Generation == 1 && existing.Fence.TransitionDigest == commit.Digest {
			if err := s.syncPointerDirectory(input.RunID); err != nil {
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
	if err := createJSONFileWithHooks(s.pointerPath(input.RunID), pointer, maxTypedObjectSize,
		func() error { return s.hit(BoundaryBeforePointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerPublish) },
		func() error { return s.hit(BoundaryAfterPointerSync) }); err != nil {
		return nil, fmt.Errorf("publish typed run pointer: %w", err)
	}
	return s.Load(input.RunID)
}

func (s *TypedRunStore) PublishSuccessor(input SuccessorInput) (*TypedRunSnapshot, error) {
	if s == nil || !opaqueid.Valid(input.RunID) || input.Expected.Generation == 0 || !validDigest(input.Expected.TransitionDigest) {
		return nil, errors.New("typed successor input is invalid")
	}
	publishedAt, err := formatEventTime(input.PublishedAt)
	if err != nil {
		return nil, errors.New("typed successor input time is invalid")
	}
	if err := s.validateRunDirectories(input.RunID); err != nil {
		return nil, err
	}
	release, err := s.acquireWriterLock(input.RunID)
	if err != nil {
		return nil, err
	}
	defer release()
	pointerRaw, err := readBoundedRegular(s.pointerPath(input.RunID))
	if err != nil {
		return nil, err
	}
	var pointer runPointer
	if err := decodeCanonicalJSON(pointerRaw, &pointer); err != nil {
		return nil, errors.New("typed run pointer is malformed")
	}
	if pointer.RepairEpoch != input.Expected.RepairEpoch || pointer.Current.Generation != input.Expected.Generation ||
		pointer.Current.TransitionDigest != input.Expected.TransitionDigest {
		current, loadErr := s.Load(input.RunID)
		if loadErr == nil && pointer.Previous != nil && pointer.RepairEpoch == input.Expected.RepairEpoch &&
			pointer.Previous.Generation == input.Expected.Generation && pointer.Previous.TransitionDigest == input.Expected.TransitionDigest {
			event, eventErr := s.loadEvent(input.RunID, current.State.EventDigest)
			if eventErr == nil && event.Type == "typed_generation_published" && event.OccurredAt == publishedAt {
				if err := s.syncPointerDirectory(input.RunID); err != nil {
					return nil, err
				}
				return current, nil
			}
		}
		return nil, ErrTypedRunFenced
	}
	current, err := s.Load(input.RunID)
	if err != nil {
		return nil, err
	}
	if current.State.Status != TypedRunInitializing && current.State.Status != TypedRunReconcileRequired {
		return nil, errors.New("typed current state cannot publish a normal successor")
	}
	var predecessor transitionCommit
	if err := s.readObject(input.RunID, "transitions", current.Fence.TransitionDigest, &predecessor); err != nil {
		return nil, err
	}
	previousEvent, err := s.loadEvent(input.RunID, predecessor.EventDigest)
	if err != nil {
		return nil, err
	}
	nextGeneration, err := checkedIncrement(current.Fence.Generation)
	if err != nil {
		return nil, err
	}
	nextSequence, err := checkedIncrement(previousEvent.Sequence)
	if err != nil {
		return nil, err
	}
	event := eventObject{
		Schema: eventSchema, RunID: input.RunID, Sequence: nextSequence, PreviousEventDigest: &predecessor.EventDigest,
		RunGeneration: nextGeneration, ProjectionRevision: current.State.ProjectionRevision,
		OccurredAt: publishedAt, Type: "typed_generation_published", Subject: input.RunID,
		EvidenceDigests: append([]string(nil), current.State.ObservationDigests...),
	}
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	state := current.State
	state.EventDigest = event.Digest
	state.ResultingStateDigest = ""
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return nil, err
	}
	previousDigest, previousGeneration, previousState := current.Fence.TransitionDigest, current.Fence.Generation, current.State.ResultingStateDigest
	previousEventDigest, previousProjectionRevision := predecessor.EventDigest, current.State.ProjectionRevision
	commit := transitionCommit{
		Schema: transitionSchema, RunID: input.RunID, RepairEpoch: current.Fence.RepairEpoch, Generation: nextGeneration,
		PreviousTransitionDigest: &previousDigest, PreviousGeneration: &previousGeneration, PreviousResultingStateDigest: &previousState,
		PreviousEventDigest: &previousEventDigest, PreviousProjectionRevision: &previousProjectionRevision,
		PacketDigest: state.PacketDigest, ObservationDigests: append([]string(nil), state.ObservationDigests...),
		ProjectionDigest: state.ProjectionDigest, EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return nil, err
	}
	priorReference := pointer.Current
	next := runPointer{
		Schema: runPointerSchema, RunID: input.RunID, RepairEpoch: current.Fence.RepairEpoch,
		Current:  TransitionReference{Generation: commit.Generation, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: &priorReference,
	}
	if err := validateGeneratedCandidate(event, commit, next, priorReference, &priorReference, current.State, current.State.Status); err != nil {
		return nil, err
	}
	if err := s.writeRecoveryObjects(input.RunID, event, commit); err != nil {
		return nil, err
	}
	if err := s.replacePointer(input.RunID, pointerRaw, next); err != nil {
		return nil, err
	}
	return s.Load(input.RunID)
}

func (s *TypedRunStore) Recover(runID string, expected TypedRunFence) (*TypedRunSnapshot, error) {
	if s == nil || !opaqueid.Valid(runID) || expected.Generation == 0 || !validDigest(expected.TransitionDigest) {
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
	if err := decodeCanonicalJSON(pointerRaw, &pointer); err != nil {
		return nil, errors.New("typed run pointer is malformed")
	}
	if pointer.Schema != runPointerSchema || pointer.RunID != runID || pointer.Current.Generation == 0 ||
		!validDigest(pointer.Current.TransitionDigest) || !validDigest(pointer.Current.ResultingStateDigest) {
		return nil, errors.New("typed run pointer is invalid")
	}
	if pointer.RepairEpoch != expected.RepairEpoch || pointer.Current.Generation != expected.Generation ||
		pointer.Current.TransitionDigest != expected.TransitionDigest {
		current, loadErr := s.Load(runID)
		if loadErr == nil {
			switch current.State.Status {
			case TypedRunQuarantined:
				var quarantine transitionCommit
				if err := s.readObject(runID, "transitions", pointer.Current.TransitionDigest, &quarantine); err == nil && recoveryMatchesExpected(quarantine, expected) {
					return s.publishRepair(runID, pointerRaw, pointer)
				}
			case TypedRunReconcileRequired:
				if pointer.Previous != nil {
					var quarantine transitionCommit
					if err := s.readObject(runID, "transitions", pointer.Previous.TransitionDigest, &quarantine); err == nil &&
						quarantine.ResultingState.Status == TypedRunQuarantined && recoveryMatchesExpected(quarantine, expected) {
						if err := s.syncPointerDirectory(runID); err != nil {
							return nil, err
						}
						return current, nil
					}
				}
			}
		}
		return nil, ErrTypedRunFenced
	}

	current, loadErr := s.Load(runID)
	if loadErr == nil {
		switch current.State.Status {
		case TypedRunQuarantined:
			return s.publishRepair(runID, pointerRaw, pointer)
		case TypedRunInitializing, TypedRunReconcileRequired:
			if err := s.syncPointerDirectory(runID); err != nil {
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
	wantInvalidGeneration, err := checkedIncrement(pointer.Previous.Generation)
	if err != nil || wantInvalidGeneration != pointer.Current.Generation {
		return nil, errors.New("typed invalid transition is not adjacent to its verified predecessor")
	}
	base, err := s.loadVerifiedReference(runID, *pointer.Previous)
	if err != nil {
		return nil, err
	}
	chain, err := s.buildRecoveryChain(runID, pointer, base)
	if err != nil {
		return nil, err
	}
	if _, err := s.publishCandidate(runID, pointerRaw, chain.quarantine); err != nil {
		return nil, err
	}
	quarantineRaw, err := readBoundedRegular(s.pointerPath(runID))
	if err != nil {
		return nil, err
	}
	return s.publishCandidate(runID, quarantineRaw, chain.repair)
}

func recoveryMatchesExpected(quarantine transitionCommit, expected TypedRunFence) bool {
	if quarantine.Quarantined == nil {
		return false
	}
	nextEpoch, err := checkedIncrement(expected.RepairEpoch)
	return err == nil && quarantine.RepairEpoch == nextEpoch && quarantine.Quarantined.Generation == expected.Generation &&
		quarantine.Quarantined.TransitionDigest == expected.TransitionDigest
}

func (s *TypedRunStore) buildRecoveryChain(runID string, pointer runPointer, base transitionCommit) (recoveryChain, error) {
	quarantine, err := s.buildQuarantineCandidate(runID, pointer, base)
	if err != nil {
		return recoveryChain{}, err
	}
	repair, err := s.buildRepairCandidate(runID, quarantine.pointer, quarantine.commit, quarantine.event)
	if err != nil {
		return recoveryChain{}, err
	}
	return recoveryChain{quarantine: quarantine, repair: repair}, nil
}

func (s *TypedRunStore) buildQuarantineCandidate(runID string, pointer runPointer, base transitionCommit) (transitionCandidate, error) {
	previousEvent, err := s.loadEvent(runID, base.EventDigest)
	if err != nil {
		return transitionCandidate{}, err
	}
	nextGeneration, err := checkedIncrement(pointer.Current.Generation)
	if err != nil {
		return transitionCandidate{}, err
	}
	nextSequence, err := checkedIncrement(previousEvent.Sequence)
	if err != nil {
		return transitionCandidate{}, err
	}
	nextEpoch, err := checkedIncrement(pointer.RepairEpoch)
	if err != nil {
		return transitionCandidate{}, err
	}
	event, err := recoveryEvent(runID, nextGeneration, nextSequence, "current_transition_quarantined", base.EventDigest, base.ObservationDigests, previousEvent.OccurredAt)
	if err != nil {
		return transitionCandidate{}, err
	}
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return transitionCandidate{}, err
	}
	state := base.ResultingState
	state.Status = TypedRunQuarantined
	state.EventDigest = event.Digest
	state.ResultingStateDigest = ""
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return transitionCandidate{}, err
	}
	invalid := pointer.Current
	recoveryBase := *pointer.Previous
	previousDigest, previousGeneration, previousState := invalid.TransitionDigest, invalid.Generation, invalid.ResultingStateDigest
	previousEventDigest, previousProjectionRevision := base.EventDigest, base.ResultingState.ProjectionRevision
	commit := transitionCommit{
		Schema: transitionSchema, RunID: runID, RepairEpoch: nextEpoch, Generation: nextGeneration,
		PreviousTransitionDigest: &previousDigest, PreviousGeneration: &previousGeneration, PreviousResultingStateDigest: &previousState,
		PreviousEventDigest: &previousEventDigest, PreviousProjectionRevision: &previousProjectionRevision,
		Quarantined: &invalid, RecoveryBase: &recoveryBase, Reason: "current_transition_invalid",
		PacketDigest: base.PacketDigest, ObservationDigests: append([]string(nil), base.ObservationDigests...),
		ProjectionDigest: base.ProjectionDigest, EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return transitionCandidate{}, err
	}
	next := runPointer{
		Schema: runPointerSchema, RunID: runID, RepairEpoch: commit.RepairEpoch,
		Current:  TransitionReference{Generation: commit.Generation, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: &recoveryBase,
	}
	if err := validateGeneratedCandidate(event, commit, next, invalid, &recoveryBase, base.ResultingState, TypedRunQuarantined); err != nil {
		return transitionCandidate{}, err
	}
	return transitionCandidate{event: event, commit: commit, pointer: next}, nil
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
	candidate, err := s.buildRepairCandidate(runID, pointer, quarantine, previousEvent)
	if err != nil {
		return nil, err
	}
	return s.publishCandidate(runID, pointerRaw, candidate)
}

func (s *TypedRunStore) buildRepairCandidate(runID string, pointer runPointer, quarantine transitionCommit, previousEvent eventObject) (transitionCandidate, error) {
	if pointer.Previous == nil {
		return transitionCandidate{}, errors.New("typed quarantine lacks a recovery base")
	}
	nextGeneration, err := checkedIncrement(pointer.Current.Generation)
	if err != nil {
		return transitionCandidate{}, err
	}
	nextSequence, err := checkedIncrement(previousEvent.Sequence)
	if err != nil {
		return transitionCandidate{}, err
	}
	event, err := recoveryEvent(runID, nextGeneration, nextSequence, "reconcile_required", quarantine.EventDigest, quarantine.ObservationDigests, previousEvent.OccurredAt)
	if err != nil {
		return transitionCandidate{}, err
	}
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		return transitionCandidate{}, err
	}
	state := quarantine.ResultingState
	state.Status = TypedRunReconcileRequired
	state.EventDigest = event.Digest
	state.ResultingStateDigest = ""
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		return transitionCandidate{}, err
	}
	previousDigest, previousGeneration, previousState := pointer.Current.TransitionDigest, pointer.Current.Generation, pointer.Current.ResultingStateDigest
	previousEventDigest, previousProjectionRevision := quarantine.EventDigest, quarantine.ResultingState.ProjectionRevision
	recoveryBase := *pointer.Previous
	commit := transitionCommit{
		Schema: transitionSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch, Generation: nextGeneration,
		PreviousTransitionDigest: &previousDigest, PreviousGeneration: &previousGeneration, PreviousResultingStateDigest: &previousState,
		PreviousEventDigest: &previousEventDigest, PreviousProjectionRevision: &previousProjectionRevision,
		RecoveryBase: &recoveryBase, PacketDigest: quarantine.PacketDigest,
		ObservationDigests: append([]string(nil), quarantine.ObservationDigests...), ProjectionDigest: quarantine.ProjectionDigest,
		EventDigest: event.Digest, ResultingState: state,
	}
	commit.Digest, err = digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		return transitionCandidate{}, err
	}
	quarantineReference := pointer.Current
	next := runPointer{
		Schema: runPointerSchema, RunID: runID, RepairEpoch: pointer.RepairEpoch,
		Current:  TransitionReference{Generation: commit.Generation, TransitionDigest: commit.Digest, ResultingStateDigest: state.ResultingStateDigest},
		Previous: &quarantineReference,
	}
	if err := validateGeneratedCandidate(event, commit, next, quarantineReference, &quarantineReference, quarantine.ResultingState, TypedRunReconcileRequired); err != nil {
		return transitionCandidate{}, err
	}
	return transitionCandidate{event: event, commit: commit, pointer: next}, nil
}

func (s *TypedRunStore) publishCandidate(runID string, pointerRaw []byte, candidate transitionCandidate) (*TypedRunSnapshot, error) {
	if err := s.writeRecoveryObjects(runID, candidate.event, candidate.commit); err != nil {
		return nil, err
	}
	if err := s.replacePointer(runID, pointerRaw, candidate.pointer); err != nil {
		return nil, err
	}
	return s.Load(runID)
}

func recoveryEvent(runID string, generation, sequence uint64, eventType, previousDigest string, observations []string, previousTime string) (eventObject, error) {
	occurredAt, err := time.Parse(time.RFC3339Nano, previousTime)
	if err != nil || occurredAt.Format(time.RFC3339Nano) != previousTime {
		return eventObject{}, errors.New("typed recovery event predecessor time is invalid")
	}
	formatted, err := formatEventTime(occurredAt.Add(time.Nanosecond))
	if err != nil {
		return eventObject{}, err
	}
	return eventObject{
		Schema: eventSchema, RunID: runID, Sequence: sequence, PreviousEventDigest: &previousDigest,
		RunGeneration: generation, ProjectionRevision: 0, OccurredAt: formatted,
		Type: eventType, Subject: runID, EvidenceDigests: append([]string(nil), observations...),
	}, nil
}

func validateEventSemantics(event eventObject) error {
	if event.Schema != eventSchema || !opaqueid.Valid(event.RunID) || event.Sequence == 0 || event.RunGeneration == 0 ||
		event.ProjectionRevision != 0 || !eventTypes[event.Type] || event.Subject != event.RunID || !validUniqueDigests(event.EvidenceDigests) ||
		(event.PreviousEventDigest != nil && !validDigest(*event.PreviousEventDigest)) {
		return errors.New("typed event semantics are invalid")
	}
	parsed, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != event.OccurredAt {
		return errors.New("typed event time is invalid")
	}
	wantDigest, err := digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil || wantDigest != event.Digest {
		return errors.New("typed event digest is invalid")
	}
	return nil
}

func formatEventTime(value time.Time) (string, error) {
	if value.IsZero() || value.Location() != time.UTC {
		return "", errors.New("typed event time must be nonzero UTC")
	}
	formatted := value.Format(time.RFC3339Nano)
	parsed, err := time.Parse(time.RFC3339Nano, formatted)
	if err != nil || parsed.Format(time.RFC3339Nano) != formatted {
		return "", errors.New("typed event time is outside the durable range")
	}
	return formatted, nil
}

var eventTypes = map[string]bool{
	"packet_run_published": true, "typed_generation_published": true,
	"current_transition_quarantined": true, "reconcile_required": true,
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
	if s == nil || !opaqueid.Valid(runID) {
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
	if err := s.validateCurrentCommit(runID, pointer, commit); err != nil {
		return nil, err
	}
	return &TypedRunSnapshot{
		Fence:    TypedRunFence{RepairEpoch: pointer.RepairEpoch, Generation: pointer.Current.Generation, TransitionDigest: pointer.Current.TransitionDigest},
		Previous: pointer.Previous, State: commit.ResultingState,
	}, nil
}

func (s *TypedRunStore) validateCurrentCommit(runID string, pointer runPointer, commit transitionCommit) error {
	switch commit.ResultingState.Status {
	case TypedRunInitializing:
		if commit.Generation == 1 {
			return s.validateGenesisCommit(runID, pointer, commit)
		}
		return s.validateSuccessorCommit(runID, pointer, commit)
	case TypedRunQuarantined:
		return s.validateQuarantineCommit(runID, pointer, commit)
	case TypedRunReconcileRequired:
		if commit.RecoveryBase != nil {
			return s.validateRepairCommit(runID, pointer, commit)
		}
		return s.validateSuccessorCommit(runID, pointer, commit)
	default:
		return errors.New("typed run status is unsupported")
	}
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
	if err := s.validateRollbackEvidence(commit.ResultingState.Rollback); err != nil {
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
	sources, err := packetSources(envelope)
	if err != nil {
		return err
	}
	observations := make([]SourceObservation, 0, len(commit.ObservationDigests))
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
		observations = append(observations, object.Observation)
	}
	binding := commit.ResultingState.SourceBundle
	rebuiltBundle, err := NewSourceBundle(binding.DeclarationDigest, binding.SourceCatalogDigest, binding.CallerRole, binding.QueryScope, observations)
	if err != nil || sourceBundleBinding(rebuiltBundle) != binding || rebuiltBundle.BundleID != envelope.BundleID ||
		rebuiltBundle.ContentSetDigest != envelope.ContentSetDigest || rebuiltBundle.DeclarationDigest != envelope.DeclarationDigest {
		return errors.New("typed packet observation set binding is invalid")
	}
	wantCatalog, err := sourceCatalogDigest(sources)
	if err != nil || wantCatalog != binding.SourceCatalogDigest {
		return errors.New("typed packet source catalog binding is invalid")
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
	wantEntries, err := buildProjectionEntries(sources, observations)
	if err != nil || !canonicalEqual(projection.Entries, wantEntries) {
		return errors.New("typed projection does not match packet sources")
	}
	var event eventObject
	if err := s.readObject(runID, "events", commit.EventDigest, &event); err != nil {
		return fmt.Errorf("load event object: %w", err)
	}
	if err := validateEventSemantics(event); err != nil || event.RunID != runID || event.Sequence != expected.Sequence || event.RunGeneration != commit.Generation ||
		event.ProjectionRevision != 0 || !sameOptionalString(event.PreviousEventDigest, expected.PreviousDigest) || event.Type != expected.Type || event.Subject != runID ||
		event.Digest != commit.EventDigest || !sameStrings(event.EvidenceDigests, commit.ObservationDigests) {
		return errors.New("typed event object is invalid")
	}
	return nil
}

func (s *TypedRunStore) validateSuccessorCommit(runID string, pointer runPointer, commit transitionCommit) error {
	if pointer.Previous == nil || commit.Quarantined != nil || commit.RecoveryBase != nil || commit.Reason != "" ||
		commit.Schema != transitionSchema || commit.RunID != runID || commit.RepairEpoch != pointer.RepairEpoch ||
		commit.Generation != pointer.Current.Generation || commit.Digest != pointer.Current.TransitionDigest ||
		commit.PreviousTransitionDigest == nil || *commit.PreviousTransitionDigest != pointer.Previous.TransitionDigest ||
		commit.PreviousGeneration == nil || *commit.PreviousGeneration != pointer.Previous.Generation ||
		commit.PreviousResultingStateDigest == nil || *commit.PreviousResultingStateDigest != pointer.Previous.ResultingStateDigest ||
		commit.PreviousEventDigest == nil || commit.PreviousProjectionRevision == nil || pointer.Previous.Generation+1 != commit.Generation {
		return errors.New("typed successor transition binding is invalid")
	}
	predecessor, err := s.loadVerifiedReference(runID, *pointer.Previous)
	if err != nil {
		return err
	}
	state := commit.ResultingState
	priorState := predecessor.ResultingState
	if predecessor.RepairEpoch != commit.RepairEpoch || state.Schema != TypedRunSchema || state.RunID != runID ||
		state.Status != priorState.Status || state.EffectsEnabled || state.PacketDigest != priorState.PacketDigest ||
		!sameStrings(state.ObservationDigests, priorState.ObservationDigests) || state.ProjectionDigest != priorState.ProjectionDigest ||
		state.ProjectionRevision != priorState.ProjectionRevision || state.Rollback != priorState.Rollback || state.SourceBundle != priorState.SourceBundle ||
		*commit.PreviousEventDigest != predecessor.EventDigest || *commit.PreviousProjectionRevision != priorState.ProjectionRevision {
		return errors.New("typed successor resulting state is invalid")
	}
	wantState, err := digestState(state)
	if err != nil || wantState != state.ResultingStateDigest || wantState != pointer.Current.ResultingStateDigest {
		return errors.New("typed successor resulting state digest is invalid")
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("typed successor transition digest is invalid")
	}
	previousEvent, err := s.loadEvent(runID, predecessor.EventDigest)
	if err != nil {
		return err
	}
	previousDigest := predecessor.EventDigest
	return s.validateCommitObjects(runID, commit, eventExpectation{
		Sequence: previousEvent.Sequence + 1, Type: "typed_generation_published", PreviousDigest: &previousDigest,
	})
}

func (s *TypedRunStore) validateQuarantineCommit(runID string, pointer runPointer, commit transitionCommit) error {
	if pointer.Previous == nil || commit.Quarantined == nil || commit.RecoveryBase == nil || commit.Reason != "current_transition_invalid" ||
		commit.Schema != transitionSchema || commit.RunID != runID || pointer.RepairEpoch == 0 || commit.RepairEpoch != pointer.RepairEpoch ||
		commit.Generation != pointer.Current.Generation || commit.Digest != pointer.Current.TransitionDigest ||
		commit.PreviousTransitionDigest == nil || *commit.PreviousTransitionDigest != commit.Quarantined.TransitionDigest ||
		commit.PreviousGeneration == nil || *commit.PreviousGeneration != commit.Quarantined.Generation ||
		commit.PreviousResultingStateDigest == nil || *commit.PreviousResultingStateDigest != commit.Quarantined.ResultingStateDigest ||
		commit.PreviousEventDigest == nil || commit.PreviousProjectionRevision == nil ||
		*commit.RecoveryBase != *pointer.Previous {
		return errors.New("typed quarantine transition binding is invalid")
	}
	invalidGeneration, incrementErr := checkedIncrement(commit.RecoveryBase.Generation)
	quarantineGeneration, quarantineErr := checkedIncrement(commit.Quarantined.Generation)
	if incrementErr != nil || quarantineErr != nil || invalidGeneration != commit.Quarantined.Generation || quarantineGeneration != commit.Generation {
		return errors.New("typed quarantine transition generations are invalid")
	}
	if err := s.validateTransitionState(pointer, commit, TypedRunQuarantined); err != nil {
		return err
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("typed quarantine transition digest is invalid")
	}
	base, err := s.loadVerifiedReference(runID, *commit.RecoveryBase)
	if err != nil {
		return err
	}
	if *commit.PreviousEventDigest != base.EventDigest || *commit.PreviousProjectionRevision != base.ResultingState.ProjectionRevision {
		return errors.New("typed quarantine recovery base is inconsistent")
	}
	expectedState := base.ResultingState
	expectedState.Status = TypedRunQuarantined
	expectedState.EventDigest = commit.EventDigest
	expectedState.ResultingStateDigest = ""
	expectedState.ResultingStateDigest, err = digestState(expectedState)
	if err != nil || !canonicalEqual(expectedState, commit.ResultingState) || commit.PacketDigest != base.PacketDigest ||
		!sameStrings(commit.ObservationDigests, base.ObservationDigests) || commit.ProjectionDigest != base.ProjectionDigest {
		return errors.New("typed quarantine state does not derive from recovery base")
	}
	baseEvent, err := s.loadEvent(runID, base.EventDigest)
	if err != nil {
		return err
	}
	previousEvent := base.ResultingState.EventDigest
	return s.validateCommitObjects(runID, commit, eventExpectation{Sequence: baseEvent.Sequence + 1, Type: "current_transition_quarantined", PreviousDigest: &previousEvent})
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
	if quarantine.RecoveryBase == nil || *commit.RecoveryBase != *quarantine.RecoveryBase {
		return errors.New("typed repair recovery base is inconsistent")
	}
	expectedState := quarantine.ResultingState
	expectedState.Status = TypedRunReconcileRequired
	expectedState.EventDigest = commit.EventDigest
	expectedState.ResultingStateDigest = ""
	expectedState.ResultingStateDigest, err = digestState(expectedState)
	if err != nil || !canonicalEqual(expectedState, commit.ResultingState) || commit.PacketDigest != quarantine.PacketDigest ||
		!sameStrings(commit.ObservationDigests, quarantine.ObservationDigests) || commit.ProjectionDigest != quarantine.ProjectionDigest {
		return errors.New("typed repair state does not derive from quarantine")
	}
	quarantineEvent, err := s.loadEvent(runID, quarantine.EventDigest)
	if err != nil {
		return err
	}
	previousEvent := quarantine.ResultingState.EventDigest
	return s.validateCommitObjects(runID, commit, eventExpectation{Sequence: quarantineEvent.Sequence + 1, Type: "reconcile_required", PreviousDigest: &previousEvent})
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

func (s *TypedRunStore) loadVerifiedReference(runID string, reference TransitionReference) (transitionCommit, error) {
	var commit transitionCommit
	if err := s.readObject(runID, "transitions", reference.TransitionDigest, &commit); err != nil {
		return transitionCommit{}, fmt.Errorf("load verified recovery base: %w", err)
	}
	pointer := runPointer{Schema: runPointerSchema, RunID: runID, RepairEpoch: commit.RepairEpoch, Current: reference}
	if commit.ResultingState.Status == TypedRunQuarantined {
		pointer.Previous = commit.RecoveryBase
	} else if commit.PreviousTransitionDigest != nil && commit.PreviousGeneration != nil && commit.PreviousResultingStateDigest != nil {
		pointer.Previous = &TransitionReference{
			Generation: *commit.PreviousGeneration, TransitionDigest: *commit.PreviousTransitionDigest,
			ResultingStateDigest: *commit.PreviousResultingStateDigest,
		}
	}
	if err := s.validateCurrentCommit(runID, pointer, commit); err != nil {
		return transitionCommit{}, fmt.Errorf("validate recovery base: %w", err)
	}
	return commit, nil
}

func (s *TypedRunStore) validateRollbackEvidence(evidence RollbackEvidence) error {
	if !safeOpaque(evidence.ArtifactVersion) || !validDigest(evidence.ArtifactDigest) || !validDigest(evidence.FixtureDigest) ||
		evidence.ReadableSchema != TypedRunSchema || !evidence.CreationDisabled {
		return errors.New("typed publication requires verified creation-disabled rollback evidence")
	}
	if s.rollback == nil {
		return errors.New("typed publication requires a trusted rollback verifier")
	}
	if err := s.rollback.VerifyRollback(evidence); err != nil {
		return errors.New("typed publication rollback evidence is untrusted")
	}
	return nil
}

func packetSources(envelope packetEnvelope) ([]source, error) {
	var sources []source
	if err := decodeStrictJSON(envelope.Sources, &sources); err != nil || len(sources) == 0 {
		return nil, errors.New("packet source descriptors are invalid")
	}
	if _, _, err := validateSources(sources); err != nil {
		return nil, errors.New("packet source descriptors are invalid")
	}
	return sources, nil
}

func sourceBundleBinding(bundle SourceBundle) SourceBundleBinding {
	return SourceBundleBinding{
		BundleID: bundle.BundleID, DeclarationDigest: bundle.DeclarationDigest, SourceCatalogDigest: bundle.SourceCatalogDigest,
		CallerRole: bundle.CallerRole, QueryScope: bundle.QueryScope, ContentSetDigest: bundle.ContentSetDigest,
	}
}

func buildProjectionEntries(sources []source, observations []SourceObservation) ([]projectionEntry, error) {
	observed := make(map[string]SourceObservation, len(observations))
	for _, observation := range observations {
		observed[observation.SourceID] = observation
	}
	entries := make([]projectionEntry, 0, len(sources))
	for _, declared := range sources {
		observation, ok := observed[declared.ID]
		if !ok || observation.Quality != QualityVerified || !sourceMatchesDeclaration(declared, observation) {
			return nil, errors.New("compiled projection source is not verified")
		}
		revision := observation.Revision
		if declared.ObservationPolicy != "" {
			revision = observation.ObservedAt
		}
		label, ok := sourceProvenanceLabels[declared.Kind]
		if !ok {
			return nil, errors.New("compiled projection source label is unsupported")
		}
		entries = append(entries, projectionEntry{
			Role: declared.Role, SourceLabel: label, SourceID: declared.ID, Locator: declared.Locator, Revision: revision,
			Reason: declared.Reason, Exposure: "bound", ObservationDigest: observation.EnvelopeDigest,
		})
	}
	return entries, nil
}

func validateProjectionEntries(entries []projectionEntry, observations []string) error {
	allowed := make(map[string]bool, len(observations))
	for _, digest := range observations {
		allowed[digest] = true
	}
	seenSources := make(map[string]bool, len(entries))
	seenObservations := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !validSlug(entry.Role) || !provenanceLabels[entry.SourceLabel] || !validSlug(entry.SourceID) || seenSources[entry.SourceID] || !safeOpaque(entry.Locator) ||
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

var sourceProvenanceLabels = map[string]string{
	"git": "git", "td": "td", "dagr": "dagr", "sergeant": "sergeant",
	"receiving-system": "receiving_system", "environment-classifier": "receiving_system",
	"validation-capability": "platoon", "platoon-policy": "platoon",
}

var provenanceLabels = map[string]bool{
	"git": true, "td": true, "dagr": true, "sergeant": true, "receiving_system": true, "platoon": true,
}

func validateTypedObjectSize(kind string, value any) error {
	raw, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	limit, err := typedObjectLimit(kind)
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return errors.New("typed state object exceeds its size limit")
	}
	return nil
}

func validateGeneratedCandidate(event eventObject, commit transitionCommit, pointer runPointer, commitPrevious TransitionReference, pointerPrevious *TransitionReference, baseState TypedRunState, status TypedRunStatus) error {
	if err := validateEventSemantics(event); err != nil {
		return err
	}
	if event.Schema != eventSchema || commit.Schema != transitionSchema || pointer.Schema != runPointerSchema ||
		event.RunID == "" || event.RunID != commit.RunID || event.RunID != pointer.RunID || event.RunGeneration != commit.Generation ||
		event.Digest != commit.EventDigest || event.ProjectionRevision != commit.ResultingState.ProjectionRevision ||
		commit.ResultingState.RunID != commit.RunID || commit.ResultingState.EffectsEnabled ||
		commit.ResultingState.PacketDigest != commit.PacketDigest || !sameStrings(commit.ResultingState.ObservationDigests, commit.ObservationDigests) ||
		commit.ResultingState.ProjectionDigest != commit.ProjectionDigest || commit.ResultingState.EventDigest != commit.EventDigest ||
		commit.PreviousTransitionDigest == nil || *commit.PreviousTransitionDigest != commitPrevious.TransitionDigest ||
		commit.PreviousGeneration == nil || *commit.PreviousGeneration != commitPrevious.Generation ||
		commit.PreviousResultingStateDigest == nil || *commit.PreviousResultingStateDigest != commitPrevious.ResultingStateDigest ||
		commit.PreviousEventDigest == nil || !sameOptionalString(event.PreviousEventDigest, commit.PreviousEventDigest) ||
		pointer.RepairEpoch != commit.RepairEpoch || pointer.Current.Generation != commit.Generation ||
		pointer.Current.TransitionDigest != commit.Digest || pointer.Current.ResultingStateDigest != commit.ResultingState.ResultingStateDigest ||
		pointerPrevious == nil || pointer.Previous == nil || *pointer.Previous != *pointerPrevious {
		return errors.New("generated typed transition binding is invalid")
	}
	expectedState := baseState
	expectedState.Status = status
	expectedState.EventDigest = event.Digest
	expectedState.ResultingStateDigest = ""
	expectedDigest, err := digestState(expectedState)
	expectedState.ResultingStateDigest = expectedDigest
	if err != nil || !canonicalEqual(expectedState, commit.ResultingState) {
		return errors.New("generated typed state does not derive from verified base")
	}
	wantState, err := digestState(commit.ResultingState)
	if err != nil || wantState != commit.ResultingState.ResultingStateDigest {
		return errors.New("generated typed state digest is invalid")
	}
	wantCommit, err := digestWithoutField(transitionSchema, commit, func(value *transitionCommit) { value.Digest = "" })
	if err != nil || wantCommit != commit.Digest {
		return errors.New("generated typed transition digest is invalid")
	}
	for _, candidate := range []struct {
		kind  string
		value any
	}{{"events", event}, {"transitions", commit}, {"pointer", pointer}} {
		if err := validateTypedObjectSize(candidate.kind, candidate.value); err != nil {
			return err
		}
	}
	return nil
}

func typedObjectLimit(kind string) (int, error) {
	switch kind {
	case "events":
		return maxEventObjectSize, nil
	case "observations":
		return maxObservationObjectSize, nil
	case "packets", "projections", "transitions", "pointer":
		return maxTypedObjectSize, nil
	default:
		return 0, errors.New("typed object kind is invalid")
	}
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

func canonicalEqual(left, right any) bool {
	leftRaw, leftErr := canonicalJSON(left)
	rightRaw, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
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

func checkedIncrement(value uint64) (uint64, error) {
	if value == ^uint64(0) {
		return 0, errors.New("typed transition counter overflow")
	}
	return value + 1, nil
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
	limit, err := typedObjectLimit(kind)
	if err != nil {
		return err
	}
	return createJSONFileWithHooks(path, value, limit, nil,
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

func createJSONFileWithHooks(path string, value any, limit int, beforePublish, afterPublish, afterSync func() error) error {
	raw, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	if len(raw) > limit {
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
	return decodeCanonicalJSON(raw, destination)
}

func decodeCanonicalJSON(raw []byte, destination any) error {
	if err := decodeStrictJSON(raw, destination); err != nil {
		return err
	}
	canonical, err := canonicalJSON(destination)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("typed authority bytes are not canonical")
	}
	return nil
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
	return readBoundedRegularLimit(path, maxTypedObjectSize)
}

func readBoundedRegularLimit(path string, limit int) ([]byte, error) {
	first, err := readBoundedRegularOnce(path, limit)
	if err != nil {
		return nil, err
	}
	second, err := readBoundedRegularOnce(path, limit)
	if err != nil || !bytes.Equal(first, second) {
		return nil, errors.New("typed state object changed while reading")
	}
	return first, nil
}

func readBoundedRegularOnce(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > int64(limit) {
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
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) > limit {
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
	limit, err := typedObjectLimit(kind)
	if err != nil {
		return err
	}
	raw, err := readBoundedRegularLimit(filepath.Join(s.runDir(runID), "objects", kind, digest+".json"), limit)
	if err != nil {
		return err
	}
	if err := decodeStrictJSON(raw, destination); err != nil {
		return err
	}
	canonical, err := canonicalJSON(destination)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("typed immutable object bytes are not canonical")
	}
	return nil
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

func (s *TypedRunStore) syncPointerDirectory(runID string) error {
	directory := filepath.Dir(s.pointerPath(runID))
	if s.pointerSync != nil {
		return s.pointerSync(directory)
	}
	return syncTypedDirectory(directory)
}
