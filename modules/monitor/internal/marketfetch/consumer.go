// Package marketfetch ingests Collector batch completion events. The Monitor
// keeps only the latest small status snapshot; detailed retry rows remain in
// Collector SQLite and Storage is still the freshness source of truth.
package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
)

type Status struct {
	SpaceID      string
	DatasetID    string
	Frequency    string
	BatchID      string
	ScheduleID   string
	BatchKind    string
	Region       string
	NodeID       string
	RequestID    string
	Status       string
	SuccessCount int32
	RetryCount   int32
	CompletedAt  time.Time
	ErrorSummary string
}

type Store struct {
	mu     sync.RWMutex
	latest map[string]Status
}

func NewStore() *Store { return &Store{latest: make(map[string]Status)} }

func (s *Store) Observe(spaceID string, payload *marketfetchpb.MarketFetchBatchCompleted) {
	if s == nil || payload == nil {
		return
	}
	completed := time.Time{}
	if payload.GetCompletedAt() != nil && payload.GetCompletedAt().CheckValid() == nil {
		completed = payload.GetCompletedAt().AsTime().UTC()
	}
	s.mu.Lock()
	s.latest[spaceID+"/"+payload.GetDatasetId()+"/"+payload.GetFrequency()] = Status{SpaceID: spaceID, DatasetID: payload.GetDatasetId(), Frequency: payload.GetFrequency(), BatchID: payload.GetBatchId(), ScheduleID: payload.GetScheduleId(), BatchKind: payload.GetBatchKind(), Region: payload.GetRegion(), NodeID: payload.GetNodeId(), RequestID: payload.GetRequestId(), Status: payload.GetStatus(), SuccessCount: payload.GetSuccessCount(), RetryCount: payload.GetRetryCount(), CompletedAt: completed, ErrorSummary: payload.GetErrorSummary()}
	s.mu.Unlock()
}

func (s *Store) Get(spaceID, datasetID, frequency string) (Status, bool) {
	if s == nil {
		return Status{}, false
	}
	s.mu.RLock()
	status, ok := s.latest[spaceID+"/"+datasetID+"/"+frequency]
	s.mu.RUnlock()
	return status, ok
}

// Snapshots returns a stable copy for the lightweight freshness timer. The
// Monitor intentionally keeps only one row per Dataset/Frequency; detailed
// batch and RetryItem history remains owned by Collector SQLite.
func (s *Store) Snapshots() []Status {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make([]Status, 0, len(s.latest))
	for _, status := range s.latest {
		out = append(out, status)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpaceID != out[j].SpaceID {
			return out[i].SpaceID < out[j].SpaceID
		}
		if out[i].DatasetID != out[j].DatasetID {
			return out[i].DatasetID < out[j].DatasetID
		}
		return out[i].Frequency < out[j].Frequency
	})
	return out
}

func (s *Store) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	count := len(s.latest)
	s.mu.RUnlock()
	return count
}

func Start(ctx context.Context, client *jetstream.Client, registry *events.Registry, store *Store) error {
	if client == nil || registry == nil || store == nil {
		return fmt.Errorf("market fetch monitor consumer dependencies are required")
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{Name: "monitor-market-fetch-v1", Event: events.MarketFetchBatchCompleted, AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 128, FetchMaxWait: time.Second, DeliverDecodeErrors: true})
	if err != nil {
		return err
	}
	go func() {
		defer consumer.Close()
		for ctx.Err() == nil {
			deliveries, fetchErr := consumer.FetchEvents(ctx, 32)
			if fetchErr != nil && len(deliveries) == 0 {
				continue
			}
			for _, delivery := range deliveries {
				if delivery == nil || delivery.Delivery == nil {
					continue
				}
				if delivery.Err != nil {
					_ = delivery.Delivery.Term(ctx)
					continue
				}
				payload, ok := delivery.Payload.(*marketfetchpb.MarketFetchBatchCompleted)
				if !ok || payload == nil {
					_ = delivery.Delivery.Term(ctx)
					continue
				}
				store.Observe(delivery.Message.GetSpaceId(), payload)
				_ = delivery.Delivery.Ack(ctx)
			}
		}
	}()
	return nil
}
