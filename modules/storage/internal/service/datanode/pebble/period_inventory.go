package pebble

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	storagegen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

const (
	periodInventoryMaxRecords = 100000
)

// PeriodLedgerScope requires an explicit identity and closed period-time range.
type PeriodLedgerScope struct {
	SpaceID       string `json:"space_id"`
	DatasetID     string `json:"dataset_id"`
	Frequency     string `json:"frequency"`
	PeriodTimeMin int64  `json:"period_time_min"`
	PeriodTimeMax int64  `json:"period_time_max"`
}

// PeriodLedgerEntry contains period metadata only; it never includes market rows
// or the snapshot's subject identifiers.
type PeriodLedgerEntry struct {
	SpaceID               string   `json:"space_id"`
	DatasetID             string   `json:"dataset_id"`
	Frequency             string   `json:"frequency"`
	PeriodTime            int64    `json:"period_time"`
	Status                string   `json:"status"`
	SeriesHash            string   `json:"series_hash"`
	ExpectedCount         uint32   `json:"expected_count"`
	DeadlineAt            int64    `json:"deadline_at"`
	ReservationID         string   `json:"reservation_id,omitempty"`
	SuccessfulSeriesCount int      `json:"successful_series_count"`
	FailedSeriesCount     int      `json:"failed_series_count"`
	SnapshotFormat        string   `json:"snapshot_format"`
	SnapshotEntryCount    int      `json:"snapshot_entry_count"`
	SnapshotSubjectCount  int      `json:"snapshot_subject_count"`
	WaitingIndexCount     int      `json:"waiting_index_count"`
	DeadlineIndexCount    int      `json:"deadline_index_count"`
	DeadlineIndexAt       int64    `json:"deadline_index_at,omitempty"`
	CompleteIndexCount    int      `json:"complete_index_count"`
	WaitingIndexPresent   bool     `json:"waiting_index_present"`
	DeadlineIndexPresent  bool     `json:"deadline_index_present"`
	CompleteIndexPresent  bool     `json:"complete_index_present"`
	MarkerRecordCount     int      `json:"marker_record_count"`
	OutboxCount           int      `json:"outbox_count"`
	IntegrityErrors       []string `json:"integrity_errors,omitempty"`
}

// PeriodLedgerInventory is an output-bounded, read-only view of one Storage period scope.
type PeriodLedgerInventory struct {
	CapturedAt           time.Time           `json:"captured_at"`
	SourcePath           string              `json:"source_path"`
	SourceLayoutVersion  string              `json:"source_layout_version"`
	SourceDataNodeID     string              `json:"source_data_node_id"`
	SourceStoreID        string              `json:"source_store_id"`
	Scope                PeriodLedgerScope   `json:"scope"`
	PeriodCount          int                 `json:"period_count"`
	WaitingCount         int                 `json:"waiting_count"`
	CompleteCount        int                 `json:"complete_count"`
	DegradedCount        int                 `json:"degraded_count"`
	IndexEntriesScanned  int                 `json:"index_entries_scanned"`
	MarkerRecordsScanned int                 `json:"marker_records_scanned"`
	OutboxEntriesScanned int                 `json:"outbox_entries_scanned"`
	IntegrityErrors      []string            `json:"integrity_errors,omitempty"`
	Periods              []PeriodLedgerEntry `json:"periods"`
}

type periodInventoryRecord struct {
	fields             map[string][]byte
	markerIDs          map[string]periodInventoryMarker
	outboxIDs          map[string]periodInventoryMarker
	deadlineIndexTimes []int64
	entry              PeriodLedgerEntry
}

type periodInventoryMarker struct {
	raw                []byte
	status             string
	batchID            string
	configSnapshotID   string
	expectedScopeRef   string
	universeSubjectIDs []string
	failedSubjects     []string
	eventIDMatches     bool
	timestampMatches   bool
}

// InspectPeriodLedger opens an existing Pebble DB read-only. Pebble's directory
// lock deliberately makes the call fail while the DataNode owns the database.
func InspectPeriodLedger(path, expectedDataNodeID string, scope PeriodLedgerScope) (inventory PeriodLedgerInventory, retErr error) {
	if err := validatePeriodLedgerScope(path, expectedDataNodeID, scope); err != nil {
		return PeriodLedgerInventory{}, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("resolve Pebble path: %w", err)
	}
	layoutData, err := os.ReadFile(filepath.Join(absPath, layoutMarkerName))
	if err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("read DataNode storage layout marker: %w", err)
	}
	if err := validateLayoutMarker(layoutData); err != nil {
		return PeriodLedgerInventory{}, err
	}
	db, err := cpebble.Open(absPath, &cpebble.Options{ReadOnly: true, Merger: BitmapORMerger})
	if err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("open Pebble database read-only (stop DataNode or use a consistent offline copy): %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil && retErr == nil {
			inventory = PeriodLedgerInventory{}
			retErr = fmt.Errorf("close read-only Pebble database: %w", err)
		}
	}()

	identityRaw, closer, err := db.Get([]byte("__meta/source_store"))
	if err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("read DataNode source identity: %w", err)
	}
	var sourceIdentity struct {
		Node string
		ID   string
	}
	identityCopy := append([]byte(nil), identityRaw...)
	if err := closer.Close(); err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("close DataNode source identity value: %w", err)
	}
	if err := json.Unmarshal(identityCopy, &sourceIdentity); err != nil {
		return PeriodLedgerInventory{}, fmt.Errorf("decode DataNode source identity: %w", err)
	}
	if sourceIdentity.Node == "" || sourceIdentity.ID == "" {
		return PeriodLedgerInventory{}, errors.New("DataNode source identity is incomplete")
	}
	if sourceIdentity.Node != expectedDataNodeID {
		return PeriodLedgerInventory{}, fmt.Errorf("DataNode store identity %q does not match requested DataNode ID %q", sourceIdentity.Node, expectedDataNodeID)
	}
	result := PeriodLedgerInventory{
		CapturedAt: time.Now().UTC(), SourcePath: absPath, SourceLayoutVersion: strings.TrimSpace(string(layoutData)),
		SourceDataNodeID: sourceIdentity.Node, SourceStoreID: sourceIdentity.ID,
		Scope: scope, Periods: make([]PeriodLedgerEntry, 0),
	}
	records := make(map[string]*periodInventoryRecord)
	lowerBound := []byte(periodBase(scope.SpaceID, scope.DatasetID, scope.Frequency, scope.PeriodTimeMin))
	upperBound := nextPrefix([]byte(periodBase(scope.SpaceID, scope.DatasetID, scope.Frequency, scope.PeriodTimeMax)))
	if err := scanPeriodProgress(db, lowerBound, upperBound, scope, records); err != nil {
		return PeriodLedgerInventory{}, err
	}
	if len(records) > periodInventoryMaxRecords {
		return PeriodLedgerInventory{}, fmt.Errorf("period inventory exceeds the %d record safety limit", periodInventoryMaxRecords)
	}
	if err := scanPeriodIndexes(db, scope, records, &result); err != nil {
		return PeriodLedgerInventory{}, err
	}
	if err := scanPeriodMarkerLinks(db, scope, records, &result); err != nil {
		return PeriodLedgerInventory{}, err
	}
	for _, record := range records {
		finalizePeriodInventoryRecord(record)
		entry := record.entry
		result.Periods = append(result.Periods, entry)
		result.PeriodCount++
		switch entry.Status {
		case "waiting":
			result.WaitingCount++
		case "complete":
			result.CompleteCount++
		case "degraded":
			result.DegradedCount++
		}
		for _, integrityErr := range entry.IntegrityErrors {
			result.IntegrityErrors = append(result.IntegrityErrors, fmt.Sprintf("%s: %s", periodIdentity(entry), integrityErr))
		}
	}
	sort.Slice(result.Periods, func(i, j int) bool { return result.Periods[i].PeriodTime < result.Periods[j].PeriodTime })
	return result, nil
}

func validatePeriodLedgerScope(path, expectedDataNodeID string, scope PeriodLedgerScope) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Pebble path is required")
	}
	if strings.TrimSpace(expectedDataNodeID) == "" || strings.TrimSpace(expectedDataNodeID) != expectedDataNodeID {
		return errors.New("expected DataNode ID is required and must not have surrounding whitespace")
	}
	for _, field := range []struct{ name, value string }{{"space_id", scope.SpaceID}, {"dataset_id", scope.DatasetID}, {"frequency", scope.Frequency}} {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%s is required and must not have surrounding whitespace", field.name)
		}
	}
	if scope.PeriodTimeMin <= 0 || scope.PeriodTimeMax < scope.PeriodTimeMin {
		return errors.New("period_time bounds must be positive and period_time_max must be greater than or equal to period_time_min")
	}
	return nil
}

func scanPeriodProgress(db *cpebble.DB, lowerBound, upperBound []byte, scope PeriodLedgerScope, records map[string]*periodInventoryRecord) error {
	iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: lowerBound, UpperBound: upperBound})
	if err != nil {
		return fmt.Errorf("iterate scoped period progress: %w", err)
	}
	iterOpen := true
	defer func() {
		if iterOpen {
			_ = iter.Close()
		}
	}()
	for valid := iter.First(); valid; valid = iter.Next() {
		base, field, err := splitPeriodFieldKey(string(iter.Key()))
		if err != nil {
			return err
		}
		spaceID, datasetID, frequency, periodTime, err := parsePeriodBase(base)
		if err != nil {
			return fmt.Errorf("parse period key %q: %w", base, err)
		}
		if spaceID != scope.SpaceID || datasetID != scope.DatasetID || frequency != scope.Frequency || periodTime < scope.PeriodTimeMin || periodTime > scope.PeriodTimeMax {
			continue
		}
		record := records[base]
		if record == nil {
			record = &periodInventoryRecord{fields: make(map[string][]byte), markerIDs: make(map[string]periodInventoryMarker), outboxIDs: make(map[string]periodInventoryMarker), entry: PeriodLedgerEntry{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime}}
			records[base] = record
		}
		if _, exists := record.fields[field]; exists {
			record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, "duplicate period field key: "+field)
			continue
		}
		record.fields[field] = append([]byte(nil), iter.Value()...)
		if len(records) > periodInventoryMaxRecords {
			return fmt.Errorf("period inventory exceeds the %d record safety limit", periodInventoryMaxRecords)
		}
	}
	iterErr := iter.Error()
	closeErr := iter.Close()
	iterOpen = false
	if iterErr != nil {
		return fmt.Errorf("iterate scoped period progress: %w", iterErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close scoped period progress iterator: %w", closeErr)
	}
	return nil
}

func splitPeriodFieldKey(key string) (string, string, error) {
	if !strings.HasPrefix(key, periodProgressPrefix) {
		return "", "", fmt.Errorf("invalid period progress key %q", key)
	}
	parts := strings.Split(strings.TrimPrefix(key, periodProgressPrefix), "/")
	if len(parts) != 5 || parts[4] == "" {
		return "", "", fmt.Errorf("invalid period progress key %q", key)
	}
	base := periodProgressPrefix + strings.Join(parts[:4], "/") + "/"
	return base, parts[4], nil
}

func scanPeriodIndexes(db *cpebble.DB, scope PeriodLedgerScope, records map[string]*periodInventoryRecord, result *PeriodLedgerInventory) error {
	indexes := []struct {
		prefix string
		field  string
	}{
		{prefix: periodWaitingPrefix, field: "waiting"},
		{prefix: periodDeadlinePrefix, field: "deadline"},
		{prefix: periodCompletePrefix, field: "complete"},
	}
	for _, index := range indexes {
		iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(index.prefix), UpperBound: nextPrefix([]byte(index.prefix))})
		if err != nil {
			return fmt.Errorf("iterate period %s indexes: %w", index.field, err)
		}
		for valid := iter.First(); valid; valid = iter.Next() {
			result.IndexEntriesScanned++
			base := string(iter.Value())
			keyBase, deadlineAt, keyErr := parsePeriodIndexKey(index.field, string(iter.Key()))
			canonical := keyErr == nil && keyBase == base && bytes.Equal(iter.Key(), canonicalPeriodIndexKey(index.field, base, deadlineAt))
			if !canonical {
				result.IntegrityErrors = append(result.IntegrityErrors, fmt.Sprintf("period %s index key is not canonical or does not match its value", index.field))
				candidates := []string{base}
				if keyBase != "" && keyBase != base {
					candidates = append(candidates, keyBase)
				}
				seen := make(map[string]struct{}, len(candidates))
				for _, candidate := range candidates {
					if _, exists := seen[candidate]; exists {
						continue
					}
					seen[candidate] = struct{}{}
					spaceID, datasetID, frequency, periodTime, candidateErr := parsePeriodBase(candidate)
					if candidateErr != nil || spaceID != scope.SpaceID || datasetID != scope.DatasetID || frequency != scope.Frequency || periodTime < scope.PeriodTimeMin || periodTime > scope.PeriodTimeMax {
						continue
					}
					if record := records[candidate]; record != nil {
						record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, index.field+" index key/value mismatch")
					} else {
						result.IntegrityErrors = append(result.IntegrityErrors, fmt.Sprintf("orphan %s index for %s", index.field, periodIdentity(PeriodLedgerEntry{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime})))
					}
				}
				continue
			}
			spaceID, datasetID, frequency, periodTime, parseErr := parsePeriodBase(base)
			if parseErr != nil {
				_ = iter.Close()
				return fmt.Errorf("parse period %s index value: %w", index.field, parseErr)
			}
			if spaceID == scope.SpaceID && datasetID == scope.DatasetID && frequency == scope.Frequency && periodTime >= scope.PeriodTimeMin && periodTime <= scope.PeriodTimeMax {
				if record := records[base]; record != nil {
					switch index.field {
					case "waiting":
						record.entry.WaitingIndexCount++
						record.entry.WaitingIndexPresent = true
					case "deadline":
						record.entry.DeadlineIndexCount++
						record.entry.DeadlineIndexPresent = true
						record.deadlineIndexTimes = append(record.deadlineIndexTimes, deadlineAt)
					case "complete":
						record.entry.CompleteIndexCount++
						record.entry.CompleteIndexPresent = true
					}
				} else {
					result.IntegrityErrors = append(result.IntegrityErrors, fmt.Sprintf("orphan %s index for %s", index.field, periodIdentity(PeriodLedgerEntry{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime})))
				}
			}
		}
		iterErr := iter.Error()
		closeErr := iter.Close()
		if iterErr != nil {
			return fmt.Errorf("iterate period %s indexes: %w", index.field, iterErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close period %s index iterator: %w", index.field, closeErr)
		}
	}
	return nil
}

func canonicalPeriodIndexKey(field, base string, deadlineAt int64) []byte {
	switch field {
	case "waiting":
		return periodWaitingKey(base)
	case "deadline":
		return periodDeadlineKey(deadlineAt, base)
	case "complete":
		return periodCompleteKey(base)
	default:
		return nil
	}
}

func parsePeriodIndexKey(field, key string) (string, int64, error) {
	var prefix string
	switch field {
	case "waiting":
		prefix = periodWaitingPrefix
	case "deadline":
		prefix = periodDeadlinePrefix
	case "complete":
		prefix = periodCompletePrefix
	default:
		return "", 0, fmt.Errorf("unknown period index field %q", field)
	}
	if !strings.HasPrefix(key, prefix) {
		return "", 0, fmt.Errorf("invalid %s index key prefix", field)
	}
	suffix := strings.TrimPrefix(key, prefix)
	deadlineAt := int64(0)
	if field == "deadline" {
		parts := strings.SplitN(suffix, "/", 2)
		if len(parts) != 2 || len(parts[0]) != 20 {
			return "", 0, fmt.Errorf("invalid deadline index key")
		}
		parsed, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return "", 0, fmt.Errorf("parse deadline index timestamp: %w", err)
		}
		deadlineAt = parsed
		suffix = parts[1]
	}
	decoded, err := hex.DecodeString(suffix)
	if err != nil {
		return "", 0, fmt.Errorf("decode %s index key: %w", field, err)
	}
	return string(decoded), deadlineAt, nil
}

func scanPeriodMarkerLinks(db *cpebble.DB, scope PeriodLedgerScope, records map[string]*periodInventoryRecord, result *PeriodLedgerInventory) error {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return fmt.Errorf("create event registry: %w", err)
	}
	markerRecordsByEventID := make(map[string]struct {
		base   string
		record *periodInventoryRecord
	})
	for _, scan := range []struct {
		prefix string
		field  string
	}{
		{prefix: markerRecordPrefix, field: "marker_record"},
		{prefix: outboxPrefix, field: "outbox"},
	} {
		iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(scan.prefix), UpperBound: nextPrefix([]byte(scan.prefix))})
		if err != nil {
			return fmt.Errorf("iterate period %s links: %w", scan.field, err)
		}
		for valid := iter.First(); valid; valid = iter.Next() {
			if scan.field == "marker_record" {
				result.MarkerRecordsScanned++
			} else {
				result.OutboxEntriesScanned++
			}
			message := &eventpb.EventMessage{}
			outboxKeyValid := true
			if scan.field == "outbox" {
				outboxKeyValid = isCanonicalOutboxKey(iter.Key())
			}
			if err := proto.Unmarshal(iter.Value(), message); err != nil {
				_ = iter.Close()
				return fmt.Errorf("decode %s event: %w", scan.field, err)
			}
			linkedMarker, hasLinkedMarker := markerRecordsByEventID[message.GetEventId()]
			if scan.field == "outbox" && hasLinkedMarker && message.GetEventName() != events.CollectorPeriodCompleted.Name() {
				linkedMarker.record.entry.OutboxCount++
				linkedMarker.record.entry.IntegrityErrors = append(linkedMarker.record.entry.IntegrityErrors, "outbox event does not match marker record")
				if !outboxKeyValid {
					linkedMarker.record.entry.IntegrityErrors = append(linkedMarker.record.entry.IntegrityErrors, "outbox key is not canonical")
				}
				continue
			}
			if message.GetEventName() != events.CollectorPeriodCompleted.Name() {
				continue
			}
			subject, err := registry.RenderSubject(events.CollectorPeriodCompleted, message.GetSpaceId(), message.GetSubjectId())
			if err != nil {
				_ = iter.Close()
				return errors.New("render period marker subject: invalid event identity")
			}
			_, marker, err := events.DecodeCollectorPeriodCompleted(registry, iter.Value(), subject, message.GetEventId())
			if err != nil {
				_ = iter.Close()
				return fmt.Errorf("validate %s period marker: invalid event payload", scan.field)
			}
			base := periodBase(message.GetSpaceId(), marker.GetDatasetId(), marker.GetFrequency(), marker.GetPeriodTime())
			inventoryMarker := periodInventoryMarker{
				raw: append([]byte(nil), iter.Value()...), status: marker.GetStatus(), batchID: marker.GetBatchId(),
				configSnapshotID: marker.GetConfigSnapshotId(), expectedScopeRef: marker.GetExpectedScopeRef(),
				universeSubjectIDs: append([]string(nil), marker.GetUniverseSubjectIds()...),
				failedSubjects:     append([]string(nil), marker.GetFailedSubjects()...),
			}
			expectedEventID, idErr := deterministicCollectorPeriodEventID(message.GetSpaceId(), marker)
			inventoryMarker.eventIDMatches = idErr == nil && expectedEventID == message.GetEventId()
			inventoryMarker.timestampMatches = message.GetOccurredAt() != nil && marker.GetCollectedAt() != nil && message.GetOccurredAt().AsTime().Equal(marker.GetCollectedAt().AsTime())
			if scan.field == "outbox" && hasLinkedMarker {
				linkedMarker.record.entry.OutboxCount++
				linkedMarker.record.outboxIDs[message.GetEventId()] = inventoryMarker
				if !outboxKeyValid {
					linkedMarker.record.entry.IntegrityErrors = append(linkedMarker.record.entry.IntegrityErrors, "outbox key is not canonical")
				}
				if base != linkedMarker.base {
					linkedMarker.record.entry.IntegrityErrors = append(linkedMarker.record.entry.IntegrityErrors, "outbox marker identity does not match marker record")
				}
				continue
			}
			if message.GetSpaceId() == scope.SpaceID && marker.GetDatasetId() == scope.DatasetID && marker.GetFrequency() == scope.Frequency && marker.GetPeriodTime() >= scope.PeriodTimeMin && marker.GetPeriodTime() <= scope.PeriodTimeMax {
				record := records[base]
				if record == nil {
					result.IntegrityErrors = append(result.IntegrityErrors, fmt.Sprintf("orphan %s marker for %s", scan.field, periodIdentity(PeriodLedgerEntry{SpaceID: scope.SpaceID, DatasetID: scope.DatasetID, Frequency: scope.Frequency, PeriodTime: marker.GetPeriodTime()})))
					continue
				}
				if scan.field == "marker_record" && !bytes.Equal(iter.Key(), datasetMarkerRecordKey(message.GetEventId())) {
					record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, "marker record key does not match event_id")
				}
				if scan.field == "outbox" && !outboxKeyValid {
					record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, "outbox key is not canonical")
				}
				if !inventoryMarker.eventIDMatches {
					record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, "marker event_id does not match deterministic payload hash")
				}
				if !inventoryMarker.timestampMatches {
					record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, "marker occurred_at does not match collected_at")
				}
				if marker.GetBatchId() != periodCompletionID(base) {
					record.entry.IntegrityErrors = append(record.entry.IntegrityErrors, scan.field+" marker has unexpected batch_id")
				}
				if scan.field == "marker_record" {
					record.entry.MarkerRecordCount++
					record.markerIDs[message.GetEventId()] = inventoryMarker
					markerRecordsByEventID[message.GetEventId()] = struct {
						base   string
						record *periodInventoryRecord
					}{base: base, record: record}
				} else {
					record.entry.OutboxCount++
					record.outboxIDs[message.GetEventId()] = inventoryMarker
				}
			}
		}
		iterErr := iter.Error()
		closeErr := iter.Close()
		if iterErr != nil {
			return fmt.Errorf("iterate period %s links: %w", scan.field, iterErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close period %s iterator: %w", scan.field, closeErr)
		}
	}
	return nil
}

func finalizePeriodInventoryRecord(record *periodInventoryRecord) {
	entry := &record.entry
	fields := record.fields
	for _, required := range []string{"status", "series_hash", "expected_count", "deadline", "bitmap", "failures"} {
		if _, ok := fields[required]; !ok {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "missing field: "+required)
		}
	}
	entry.Status = string(fields["status"])
	entry.SeriesHash = string(fields["series_hash"])
	entry.ReservationID = string(fields["reservation_id"])
	if len(fields["expected_count"]) == 4 {
		entry.ExpectedCount = uint32(fields["expected_count"][0])<<24 | uint32(fields["expected_count"][1])<<16 | uint32(fields["expected_count"][2])<<8 | uint32(fields["expected_count"][3])
	} else if _, ok := fields["expected_count"]; ok {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "expected_count must contain 4 bytes")
	}
	if len(fields["deadline"]) == 8 {
		var deadline uint64
		for _, value := range fields["deadline"] {
			deadline = deadline<<8 | uint64(value)
		}
		entry.DeadlineAt = int64(deadline)
	} else if _, ok := fields["deadline"]; ok {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "deadline must contain 8 bytes")
	}
	entry.SuccessfulSeriesCount = bitmapCount(fields["bitmap"])
	entry.FailedSeriesCount = bitmapCount(fields["failures"])
	wantBitmapBytes := bitmapSize(entry.ExpectedCount)
	if len(fields["bitmap"]) != wantBitmapBytes {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "success bitmap size does not match expected_count")
	}
	if len(fields["failures"]) != wantBitmapBytes {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "failure bitmap size does not match expected_count")
	}
	if bitmapHasPaddingBits(fields["bitmap"], entry.ExpectedCount) {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "success bitmap has bits outside expected_count")
	}
	if bitmapHasPaddingBits(fields["failures"], entry.ExpectedCount) {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "failure bitmap has bits outside expected_count")
	}
	for index := uint32(0); index < entry.ExpectedCount; index++ {
		if bitmapBitSet(fields["bitmap"], index) && bitmapBitSet(fields["failures"], index) {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "success and failure bitmaps overlap")
			break
		}
	}
	var snapshotKey string
	_, hasSeriesSnapshot := fields["series_snapshot"]
	_, hasLegacyRoster := fields["roster"]
	if hasSeriesSnapshot && hasLegacyRoster {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "both series_snapshot and legacy roster keys are present")
	}
	switch {
	case hasSeriesSnapshot:
		snapshotKey, entry.SnapshotFormat = "series_snapshot", "series_snapshot"
	case hasLegacyRoster:
		snapshotKey, entry.SnapshotFormat = "roster", "legacy_roster"
	default:
		entry.SnapshotFormat = "missing"
		entry.IntegrityErrors = append(entry.IntegrityErrors, "missing field: series_snapshot")
	}
	var markerUniverse, markerFailed []string
	markerSnapshotUsable := false
	if snapshotKey != "" {
		var series []DatasetPeriodSeries
		if err := json.Unmarshal(fields[snapshotKey], &series); err != nil {
			entry.SnapshotFormat = "invalid"
			entry.IntegrityErrors = append(entry.IntegrityErrors, "snapshot JSON is invalid: "+err.Error())
		} else {
			entry.SnapshotEntryCount = len(series)
			seen := make(map[string]struct{}, len(series))
			for _, item := range series {
				if item.SubjectID != "" {
					seen[item.SubjectID] = struct{}{}
				}
			}
			entry.SnapshotSubjectCount = len(seen)
			if len(series) != int(entry.ExpectedCount) {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "snapshot entry count does not match expected_count")
			}
			if entry.SnapshotFormat == "series_snapshot" {
				expectation := DatasetPeriodExpectation{
					SpaceID: entry.SpaceID, DatasetID: entry.DatasetID, Frequency: entry.Frequency,
					PeriodTime: entry.PeriodTime, SeriesHash: entry.SeriesHash, ExpectedCount: entry.ExpectedCount,
					DeadlineAt: entry.DeadlineAt, ReservationID: entry.ReservationID, SeriesSnapshot: series,
				}
				if _, err := normalizePeriodExpectationWithSeriesSnapshot(expectation); err != nil {
					entry.IntegrityErrors = append(entry.IntegrityErrors, "snapshot is invalid: "+err.Error())
				} else {
					markerUniverse, markerFailed = periodMarkerSubjects(expectation, fields["bitmap"], fields["failures"])
					markerSnapshotUsable = true
				}
			}
		}
	}
	if entry.SnapshotFormat == "legacy_roster" {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "legacy roster state requires an explicitly approved rebuild")
	}
	if entry.DeadlineAt <= 0 {
		if entry.Status == "degraded" {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "degraded period has no positive deadline")
		} else {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "period has no positive deadline")
		}
	}
	if entry.Status == "waiting" {
		if entry.WaitingIndexCount != 1 {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "waiting period does not have exactly one waiting index")
		}
		if entry.DeadlineIndexCount != 1 {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "waiting period does not have exactly one deadline index")
		}
		for _, indexDeadline := range record.deadlineIndexTimes {
			entry.DeadlineIndexAt = indexDeadline
			if indexDeadline != entry.DeadlineAt {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "deadline index timestamp does not match persisted deadline")
			}
		}
		if entry.CompleteIndexCount != 0 {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "waiting period has a complete index")
		}
	} else if entry.WaitingIndexCount != 0 || entry.DeadlineIndexCount != 0 || entry.CompleteIndexCount != 0 {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "terminal period retains a secondary index")
	}
	if entry.Status == "complete" && !bitmapAllSet(fields["bitmap"], entry.ExpectedCount) {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "complete period does not have all success bits")
	}
	if entry.Status == "degraded" && bitmapAllSet(fields["bitmap"], entry.ExpectedCount) {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "degraded period has all success bits")
	}
	if (entry.Status == "complete" || entry.Status == "degraded") && entry.MarkerRecordCount != 1 {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "terminal period marker record count is not 1")
	}
	if entry.OutboxCount > 1 {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "period has multiple pending outbox entries")
	}
	for eventID, marker := range record.markerIDs {
		if marker.status != entry.Status {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "marker record status does not match period status")
		}
		if marker.batchID != periodCompletionID(periodBase(entry.SpaceID, entry.DatasetID, entry.Frequency, entry.PeriodTime)) {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "marker record has unexpected batch_id")
		}
		if marker.configSnapshotID != entry.SeriesHash || marker.expectedScopeRef != entry.SeriesHash {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "marker config_snapshot_id or expected_scope_ref does not match series_hash")
		}
		if markerSnapshotUsable {
			if !equalStrings(marker.universeSubjectIDs, normalizedIDs(markerUniverse)) {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "marker universe_subject_ids do not match period snapshot")
			}
			if !equalStrings(marker.failedSubjects, normalizedIDs(markerFailed)) {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "marker failed_subjects do not match period failure bitmap")
			}
		}
		if outboxMarker, pending := record.outboxIDs[eventID]; pending {
			if !bytes.Equal(marker.raw, outboxMarker.raw) {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "marker record and outbox payloads differ")
			}
			if outboxMarker.status != marker.status {
				entry.IntegrityErrors = append(entry.IntegrityErrors, "marker record and outbox statuses differ")
			}
		}
	}
	for eventID := range record.outboxIDs {
		if _, exists := record.markerIDs[eventID]; !exists {
			entry.IntegrityErrors = append(entry.IntegrityErrors, "outbox marker has no matching marker record")
		}
	}
	if entry.Status == "waiting" && entry.MarkerRecordCount != 0 {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "waiting period already has a terminal marker")
	}
	if entry.Status != "waiting" && entry.Status != "complete" && entry.Status != "degraded" {
		entry.IntegrityErrors = append(entry.IntegrityErrors, "unknown period status")
	}
}

func deterministicCollectorPeriodEventID(spaceID string, marker *storageeventpb.CollectorPeriodCompleted) (string, error) {
	positions := make([]*storagegen.CommittedPosition, 0, len(marker.GetCommittedPositions()))
	for _, position := range marker.GetCommittedPositions() {
		if position == nil {
			continue
		}
		positions = append(positions, &storagegen.CommittedPosition{NodeId: position.GetNodeId(), StoreId: position.GetStoreId(), Sequence: position.GetSequence()})
	}
	_, eventID, err := BuildCollectorPeriodCompletedMessage(spaceID, &storagegen.CollectorPeriodCompletedMarker{
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), BatchId: marker.GetBatchId(), ConfigSnapshotId: marker.GetConfigSnapshotId(),
		ExpectedScopeRef: marker.GetExpectedScopeRef(), UniverseSubjectIds: marker.GetUniverseSubjectIds(),
		FailedSubjects: marker.GetFailedSubjects(), CommittedPositions: positions, CollectedAt: marker.GetCollectedAt(),
	})
	return eventID, err
}

func bitmapHasPaddingBits(bitmap []byte, expectedCount uint32) bool {
	if len(bitmap) != bitmapSize(expectedCount) || expectedCount%8 == 0 || len(bitmap) == 0 {
		return false
	}
	validMask := byte((1 << (expectedCount % 8)) - 1)
	return bitmap[len(bitmap)-1]&^validMask != 0
}

func isCanonicalOutboxKey(key []byte) bool {
	name := strings.TrimPrefix(string(key), outboxPrefix)
	if name == string(key) {
		return false
	}
	id, err := strconv.ParseUint(name, 10, 64)
	return err == nil && id > 0 && string(key) == outboxKey(id)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func periodIdentity(entry PeriodLedgerEntry) string {
	return fmt.Sprintf("%s/%s/%s/%d", entry.SpaceID, entry.DatasetID, entry.Frequency, entry.PeriodTime)
}
