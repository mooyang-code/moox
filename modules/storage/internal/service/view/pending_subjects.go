package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
)

type pendingSubject struct {
	Space, View, Dataset string
	Row, Message         []byte
	Node, Store          string
	Sequence             uint64
	Contract, Primary    string
}

func (s *Service) persistPendingSubjects(ctx context.Context, view viewRef, index, dataset string, rows []*pb.RowFieldUpsert, origins map[*pb.RowFieldUpsert]rowOrigin) error {
	if len(origins) == 0 {
		return nil
	}
	if s.pendingSubjectsDir == "" {
		return fmt.Errorf("pending subject journal is unavailable")
	}
	s.mu.RLock()
	schema := s.schemas[index]
	s.mu.RUnlock()
	if schema.SchemaHash == "" || schema.PrimaryDatasetID == "" {
		return fmt.Errorf("pending subject input contract is unavailable")
	}
	contract := schema.SchemaHash + ":" + strconv.FormatUint(schema.ViewVersion, 10)
	if err := os.MkdirAll(s.pendingSubjectsDir, 0o700); err != nil {
		return err
	}
	if err := syncPendingDirectory(filepath.Dir(s.pendingSubjectsDir)); err != nil {
		return err
	}
	for _, row := range rows {
		if row.GetKey().GetTimeSeries() == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		origin := origins[row]
		if origin.message == nil || origin.nodeID == "" || origin.storeID == "" || origin.sequence == 0 {
			return fmt.Errorf("pending subject provenance is incomplete")
		}
		rowData, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.RowFieldUpsert{Key: row.GetKey()})
		if err != nil {
			return err
		}
		messageData, err := proto.MarshalOptions{Deterministic: true}.Marshal(&eventpb.EventMessage{EventId: origin.message.GetEventId(), SpaceId: view.spaceID, SubjectId: dataset})
		if err != nil {
			return err
		}
		data, err := json.Marshal(pendingSubject{Space: view.spaceID, View: view.viewID, Dataset: dataset, Row: rowData, Message: messageData, Node: origin.nodeID, Store: origin.storeID, Sequence: origin.sequence, Contract: contract, Primary: schema.PrimaryDatasetID})
		if err != nil {
			return err
		}
		keyData, err := proto.MarshalOptions{Deterministic: true}.Marshal(row.GetKey())
		if err != nil {
			return err
		}
		id := stableViewEventID("pending-subject", view.spaceID, view.viewID, contract, dataset, origin.nodeID, origin.storeID, strconv.FormatUint(origin.sequence, 10), origin.message.GetEventId(), string(keyData))
		if err := writePendingSubject(s.pendingSubjectsDir, id+".json", data); err != nil {
			return err
		}
	}
	return nil
}

func writePendingSubject(dir, name string, data []byte) error {
	file, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncPendingDirectory(dir)
}

func syncPendingDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// ReplayPendingSubjects never reapplies old values. It verifies that the
// current active index contains the row before emitting the pending trigger.
func (s *Service) ReplayPendingSubjects(ctx context.Context) error {
	if s.pendingSubjectsDir == "" {
		return nil
	}
	s.mu.RLock()
	publisherReady := s.readyPublisher != nil
	s.mu.RUnlock()
	if !publisherReady {
		return nil
	}
	s.mu.Lock()
	if s.pendingSubjectsGate == nil {
		s.pendingSubjectsGate = newIndexWriteGate()
	}
	gate := s.pendingSubjectsGate
	s.mu.Unlock()
	release, err := gate.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	entries, err := os.ReadDir(s.pendingSubjectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("pending subject journal contains a non-regular record")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.pendingSubjectsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var pending pendingSubject
		if err := json.Unmarshal(data, &pending); err != nil {
			return err
		}
		row, message := new(pb.RowFieldUpsert), new(eventpb.EventMessage)
		if err := proto.Unmarshal(pending.Row, row); err != nil {
			return err
		}
		if err := proto.Unmarshal(pending.Message, message); err != nil {
			return err
		}
		if pending.Space == "" || pending.View == "" || pending.Dataset == "" || pending.Node == "" || pending.Store == "" || pending.Sequence == 0 || message.GetEventId() == "" || message.GetSpaceId() != pending.Space || message.GetSubjectId() != pending.Dataset || row.GetKey().GetSpaceId() != pending.Space || row.GetKey().GetDatasetId() != pending.Dataset || row.GetKey().GetTimeSeries() == nil {
			return fmt.Errorf("pending subject journal record has invalid identity")
		}
		view := viewRef{spaceID: pending.Space, viewID: pending.View}
		s.mu.RLock()
		runtime := s.views[view]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		index := runtime.active
		if index == "" {
			runtime.mu.Unlock()
			continue
		}
		s.mu.RLock()
		schema := s.schemas[index]
		metadata := s.catalogViews[view]
		s.mu.RUnlock()
		obsolete := pending.Contract != schema.SchemaHash+":"+strconv.FormatUint(schema.ViewVersion, 10) || pending.Primary != schema.PrimaryDatasetID
		if metadata != nil {
			keep, keepErr := time.ParseDuration(metadata.GetKeepDuration())
			at, atErr := time.Parse(time.RFC3339Nano, row.GetKey().GetTimeSeries().GetDataTime())
			obsolete = obsolete || (keepErr == nil && atErr == nil && keep > 0 && at.Before(time.Now().Add(-keep)))
		}
		if obsolete {
			err := os.Remove(path)
			if err == nil {
				err = syncPendingDirectory(s.pendingSubjectsDir)
			}
			runtime.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		key := proto.Clone(row.GetKey()).(*pb.RowKey)
		key.DatasetId = schema.PrimaryDatasetID
		found, readErr := s.query(ctx, index, []*pb.RowKey{key}, nil)
		if readErr != nil {
			runtime.mu.Unlock()
			return readErr
		}
		if len(found) == 0 {
			runtime.mu.Unlock()
			continue
		}
		err = s.publishSubjectReady(ctx, view, index, pending.Dataset, []*pb.RowFieldUpsert{row}, map[*pb.RowFieldUpsert]rowOrigin{row: {message: message, nodeID: pending.Node, storeID: pending.Store, sequence: pending.Sequence}})
		if err != nil {
			runtime.mu.Unlock()
			return err
		}
		if err := os.Remove(path); err != nil {
			runtime.mu.Unlock()
			return err
		}
		err = syncPendingDirectory(s.pendingSubjectsDir)
		runtime.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
