package coverage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type BucketReader interface {
	PresentBuckets(context.Context, string, string, string, marketdata.Frequency, time.Time, time.Time) ([]time.Time, error)
}
type StateWriter interface {
	WriteCoverageState(context.Context, State) error
}
type RepairSink interface {
	EnqueueRepair(context.Context, RepairRequest) error
}

type State struct {
	SpaceID, DatasetID, SubjectID, PartitionID string
	Frequency                                  marketdata.Frequency
	Start, End                                 time.Time
	Expected, Present, Missing                 int
	MissingRanges                              []Range
	Status                                     string
	CheckedAt                                  time.Time
}
type RepairRequest struct {
	ID, SpaceID, DatasetID, SubjectID string
	Frequency                         marketdata.Frequency
	Start, End                        time.Time
}
type Request struct {
	SpaceID, DatasetID, SubjectID, PartitionID string
	Frequency                                  marketdata.Frequency
	Start, End                                 time.Time
	Sessions                                   []Session
}

type Reconciler struct {
	Reader  BucketReader
	States  StateWriter
	Repairs RepairSink
	Now     func() time.Time
}

func (r Reconciler) Reconcile(ctx context.Context, request Request) (State, error) {
	if r.Reader == nil || r.States == nil || r.Repairs == nil {
		return State{}, fmt.Errorf("reader, state writer and repair sink are required")
	}
	expected, err := ExpectedBuckets(request.Start.UTC(), request.End.UTC(), request.Frequency, request.Sessions)
	if err != nil {
		return State{}, err
	}
	present, err := r.Reader.PresentBuckets(ctx, request.SpaceID, request.DatasetID, request.SubjectID, request.Frequency, request.Start.UTC(), request.End.UTC())
	if err != nil {
		return State{}, err
	}
	missing := MissingRanges(expected, present)
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	state := State{SpaceID: request.SpaceID, DatasetID: request.DatasetID, SubjectID: request.SubjectID, PartitionID: request.PartitionID, Frequency: request.Frequency, Start: request.Start.UTC(), End: request.End.UTC(), Expected: len(expected), Present: len(expected) - rangeBuckets(missing), Missing: rangeBuckets(missing), MissingRanges: missing, Status: "complete", CheckedAt: now}
	if state.Missing > 0 {
		state.Status = "incomplete"
	}
	if err := r.States.WriteCoverageState(ctx, state); err != nil {
		return State{}, err
	}
	for _, value := range missing {
		repair := RepairRequest{ID: repairID(request, value), SpaceID: request.SpaceID, DatasetID: request.DatasetID, SubjectID: request.SubjectID, Frequency: request.Frequency, Start: value.Start, End: value.End}
		if err := r.Repairs.EnqueueRepair(ctx, repair); err != nil {
			return State{}, err
		}
	}
	return state, nil
}

func rangeBuckets(values []Range) int {
	total := 0
	for _, value := range values {
		total += value.Buckets
	}
	return total
}
func repairID(request Request, value Range) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", request.SpaceID, request.DatasetID, request.SubjectID, request.Frequency, value.Start.UTC().Format(time.RFC3339Nano), value.End.UTC().Format(time.RFC3339Nano))))
	return hex.EncodeToString(sum[:])
}
