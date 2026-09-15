package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/merge/internal/domain"
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

type DatasetSubjectLister interface {
	ListActiveDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

type Assembler struct {
	ledger    *Ledger
	def       domain.MergedDataset
	committer InputCommitter
	required  map[string][]string
	periods   *PeriodLedger
	subjects  DatasetSubjectLister
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

func (a *Assembler) SetSubjectLister(lister DatasetSubjectLister) {
	if a != nil {
		a.subjects = lister
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
		DatasetID: a.DatasetID(), SnapshotID: a.SnapshotID(), SubjectID: canonicalCryptoSubjectID(a.SpaceID(), subjectID),
		Frequency: frequency, PeriodTime: periodTime.UTC(), SeriesTag: seriesTag,
	}
}

func (a *Assembler) ApplyArrival(ctx context.Context, key RowKey, sourceDatasetID string, fields map[string]float64) error {
	if a == nil {
		return fmt.Errorf("merge assembler is not initialized")
	}
	key.SubjectID = canonicalCryptoSubjectID(a.SpaceID(), key.SubjectID)
	if a.def.MergeMode == domain.MergeModeCustom {
		return nil
	}
	wanted, ok := a.required[strings.TrimSpace(sourceDatasetID)]
	if !ok {
		return fmt.Errorf("source dataset %q is not part of mdataset %s", sourceDatasetID, a.def.DatasetID)
	}
	if a.periods != nil {
		period := PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime}
		if err := a.ensurePeriodUniverse(ctx, period, key.Frequency); err != nil {
			return err
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
	if err != nil {
		return err
	}
	if done {
		return a.replayPeriodCommit(ctx, key)
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
	if err := a.notePeriodCommit(ctx, key, receipt); err != nil {
		return err
	}
	return a.ledger.RecordCommit(ctx, commitID, key, receipt)
}

func (a *Assembler) notePeriodCommit(ctx context.Context, key RowKey, receipt WriteReceipt) error {
	if a == nil || a.periods == nil {
		return nil
	}
	return a.periods.NoteCommit(ctx, PeriodKey{
		DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime,
	}, key.SubjectID, receipt)
}

func (a *Assembler) replayPeriodCommit(ctx context.Context, key RowKey) error {
	if a == nil || a.periods == nil {
		return nil
	}
	receipt, ok, err := a.ledger.LookupCommit(ctx, key)
	if err != nil || !ok {
		return err
	}
	if strings.TrimSpace(receipt.NodeID) == "" || strings.TrimSpace(receipt.StoreID) == "" || receipt.Sequence == 0 {
		return nil
	}
	return a.notePeriodCommit(ctx, key, receipt)
}

func periodFreezeDeadline(periodTime time.Time, frequency string) (time.Time, error) {
	end, err := domain.NextPeriod(periodTime.UTC(), frequency)
	if err != nil {
		return time.Time{}, err
	}
	return end.Add(2 * time.Minute), nil
}

func (a *Assembler) usesMembershipUniverse() bool {
	return a != nil && strings.EqualFold(strings.TrimSpace(a.def.UniverseSource), domain.UniverseSourceMembership)
}

func (a *Assembler) ensurePeriodUniverse(ctx context.Context, period PeriodKey, frequency string) error {
	if a == nil || a.periods == nil {
		return nil
	}
	if len(a.def.ObjectSet) > 0 {
		deadline, err := periodFreezeDeadline(period.PeriodTime, frequency)
		if err != nil {
			return err
		}
		return a.periods.Freeze(ctx, period, a.def.ObjectSet, deadline)
	}
	if !a.usesMembershipUniverse() {
		return nil
	}
	return a.freezeMembershipUniverse(ctx, period, frequency)
}

func (a *Assembler) freezeMembershipUniverse(ctx context.Context, period PeriodKey, frequency string) error {
	if a.subjects == nil {
		return fmt.Errorf("membership universe requires ListDatasetSubjects")
	}
	frozen, err := a.periods.Frozen(ctx, period)
	if err != nil || frozen {
		return err
	}
	for _, source := range a.def.Sources {
		ids, err := a.subjects.ListActiveDatasetSubjects(ctx, a.SpaceID(), source.DatasetID)
		if err != nil {
			return err
		}
		if err := a.periods.NoteCollectorCompleted(ctx, a.DatasetID(), source.DatasetID, period.PeriodTime, ids); err != nil {
			return err
		}
	}
	universe, ready, err := a.periods.CollectorUniverse(ctx, a.DatasetID(), a.SourceDatasetIDs(), period.PeriodTime)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("membership universe is incomplete")
	}
	deadline, err := periodFreezeDeadline(period.PeriodTime, frequency)
	if err != nil {
		return err
	}
	return a.periods.Freeze(ctx, period, universe, deadline)
}

func (a *Assembler) NoteCollectorCompleted(ctx context.Context, sourceDatasetID string, periodTime time.Time, expected []string) error {
	if a == nil || a.periods == nil {
		return nil
	}
	if a.usesMembershipUniverse() {
		return nil
	}
	if err := a.periods.NoteCollectorCompleted(ctx, a.DatasetID(), sourceDatasetID, periodTime, expected); err != nil {
		return err
	}
	if len(a.def.ObjectSet) > 0 {
		return nil
	}
	universe, ready, err := a.periods.CollectorUniverse(ctx, a.DatasetID(), a.SourceDatasetIDs(), periodTime)
	if err != nil || !ready {
		return err
	}
	deadline, err := periodFreezeDeadline(periodTime, firstNonEmpty(a.def.Frequency, "1m"))
	if err != nil {
		return err
	}
	return a.periods.Freeze(ctx, PeriodKey{
		DatasetID: a.DatasetID(), SnapshotID: a.SnapshotID(), Frequency: firstNonEmpty(a.def.Frequency, "1m"), PeriodTime: periodTime.UTC(),
	}, universe, deadline)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func canonicalCryptoSubjectID(spaceID, subjectID string) string {
	value := strings.TrimSpace(subjectID)
	if !strings.EqualFold(strings.TrimSpace(spaceID), "crypto") {
		return value
	}
	return canonicalUniverseSubjectID(value)
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
