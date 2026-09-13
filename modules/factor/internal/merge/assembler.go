package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

type RowKey struct {
	DatasetID  string
	SnapshotID string
	SubjectID  string
	Frequency  string
	PeriodTime time.Time
	SeriesTag  string
}

type InputCommitter interface {
	CommitInput(ctx context.Context, commitID string, key RowKey, fields map[string]float64, ready bool) (WriteReceipt, error)
}

type Assembler struct {
	ledger    *Ledger
	def       domain.MergedDataset
	committer InputCommitter
	required  map[string][]string
	periods   *PeriodLedger
}

func NewAssembler(ledger *Ledger, def domain.MergedDataset, committer InputCommitter) (*Assembler, error) {
	if ledger == nil {
		return nil, fmt.Errorf("merge ledger is required")
	}
	if err := domain.ValidateMergedDataset(def); err != nil {
		return nil, err
	}
	required := make(map[string][]string, len(def.Sources))
	for _, source := range def.Sources {
		required[source.DatasetID] = append([]string(nil), source.Fields...)
	}
	return &Assembler{ledger: ledger, def: def, committer: committer, required: required}, nil
}

func (a *Assembler) SetPeriodLedger(periods *PeriodLedger) {
	if a != nil {
		a.periods = periods
	}
}

func (a *Assembler) DatasetID() string {
	if a == nil {
		return ""
	}
	return a.def.DatasetID
}

func (a *Assembler) SnapshotID() string {
	if a == nil {
		return ""
	}
	return a.def.ConfigSnapshotID
}

func (a *Assembler) SpaceID() string {
	if a == nil {
		return ""
	}
	return a.def.SpaceID
}

func (a *Assembler) SourceDatasetIDs() []string {
	if a == nil {
		return nil
	}
	ids := make([]string, 0, len(a.def.Sources))
	for _, source := range a.def.Sources {
		ids = append(ids, source.DatasetID)
	}
	return ids
}

func (a *Assembler) TargetFields() []string {
	if a == nil {
		return nil
	}
	fields := make([]string, 0, len(a.def.FieldMappings))
	for _, mapping := range a.def.FieldMappings {
		fields = append(fields, mapping.TargetField)
	}
	return fields
}

func (a *Assembler) rowKey(subjectID, frequency, seriesTag string, periodTime time.Time) RowKey {
	return RowKey{
		DatasetID: a.DatasetID(), SnapshotID: a.SnapshotID(), SubjectID: subjectID,
		Frequency: frequency, PeriodTime: periodTime.UTC(), SeriesTag: seriesTag,
	}
}

func (a *Assembler) ApplyArrival(ctx context.Context, key RowKey, sourceDatasetID string, fields map[string]float64) error {
	if a == nil {
		return fmt.Errorf("merge assembler is not initialized")
	}
	if a.def.MergeMode == domain.MergeModeCustom {
		return nil
	}
	wanted, ok := a.required[strings.TrimSpace(sourceDatasetID)]
	if !ok {
		return fmt.Errorf("source dataset %q is not part of mdataset %s", sourceDatasetID, a.def.DatasetID)
	}
	if a.periods != nil {
		period := PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime}
		if len(a.def.ObjectSet) > 0 {
			if err := a.periods.Freeze(ctx, period, a.def.ObjectSet, key.PeriodTime.Add(2*time.Minute)); err != nil {
				return err
			}
		}
		accepted, err := a.periods.Accepts(ctx, period, key.SubjectID)
		if err != nil {
			return err
		}
		if !accepted {
			return nil
		}
	}
	complete := sourceComplete(wanted, fields)
	if err := a.ledger.SaveArrival(ctx, key, sourceDatasetID, fields, complete); err != nil {
		return err
	}
	return a.commitIfReady(ctx, key)
}

func (a *Assembler) commitIfReady(ctx context.Context, key RowKey) error {
	done, err := a.ledger.HasCommit(ctx, key)
	if err != nil || done {
		return err
	}
	arrivals, complete, err := a.ledger.LoadArrivals(ctx, key)
	if err != nil {
		return err
	}
	merged := make(map[string]float64)
	for _, source := range a.def.Sources {
		if !complete[source.DatasetID] {
			return nil
		}
		values := arrivals[source.DatasetID]
		if !sourceComplete(source.Fields, values) {
			return nil
		}
		for _, field := range source.Fields {
			merged[domain.MappedSourceField(source.DatasetID, field)] = values[field]
		}
	}
	commitID := stableCommitID(key)
	if a.committer == nil {
		return fmt.Errorf("input committer is required")
	}
	receipt, err := a.committer.CommitInput(ctx, commitID, key, merged, true)
	if err != nil {
		return err
	}
	if err := a.ledger.RecordCommit(ctx, commitID, key); err != nil {
		return err
	}
	if a.periods != nil {
		return a.periods.NoteCommit(ctx, PeriodKey{
			DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime,
		}, key.SubjectID, receipt)
	}
	return nil
}

func sourceComplete(required []string, fields map[string]float64) bool {
	if len(required) == 0 {
		return false
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return false
		}
	}
	return true
}

func stableCommitID(key RowKey) string {
	payload := strings.Join([]string{
		key.DatasetID, key.SnapshotID, key.SubjectID, key.Frequency,
		strconv.FormatInt(key.PeriodTime.UTC().UnixNano(), 10), key.SeriesTag,
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16])
}
