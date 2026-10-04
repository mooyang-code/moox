package test

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type storedKlineRow struct {
	RowKey string `gorm:"primaryKey"`
	Close  float64
}

type sqliteRowUpserter struct {
	db      *gorm.DB
	upserts atomic.Int32
}

type redeliveryActionReporterFunc func(
	context.Context,
	*jetstream.Delivery,
	jetstream.HandlerResult,
	error,
)

func (f redeliveryActionReporterFunc) ReportAction(
	ctx context.Context,
	delivery *jetstream.Delivery,
	result jetstream.HandlerResult,
	err error,
) {
	f(ctx, delivery, result, err)
}

func newSQLiteRowUpserter(t *testing.T) *sqliteRowUpserter {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "storage-redelivery.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&storedKlineRow{}); err != nil {
		t.Fatal(err)
	}
	return &sqliteRowUpserter{db: db}
}

func (s *sqliteRowUpserter) upsert(key *storagepb.RowKey, closeValue float64) error {
	rawKey, err := proto.MarshalOptions{Deterministic: true}.Marshal(key)
	if err != nil {
		return err
	}
	s.upserts.Add(1)
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "row_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"close"}),
	}).Create(&storedKlineRow{
		RowKey: base64.RawURLEncoding.EncodeToString(rawKey),
		Close:  closeValue,
	}).Error
}

func TestStorageRedeliveryE2EKeepsOneRowAfterLostAck(t *testing.T) {
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()

	const (
		spaceID = batchTestSpaceID + "-storage"
		itemID  = "storage-redelivery"
	)
	registry, publisher, consumer := newBatchE2EQueue(t, setupCtx, spaceID, batchTestSubject, 1)
	publishBatchE2ECompletion(t, setupCtx, publisher, spaceID, batchTestSubject, itemID)

	store := newSQLiteRowUpserter(t)
	rowKey := &storagepb.RowKey{
		SpaceId:   spaceID,
		DatasetId: "spot_kline_1m",
		Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT",
			Freq:      "1m",
			DataTime:  "2026-07-27T00:00:00Z",
		}},
	}

	firstCtx, cancelFirst := context.WithCancel(setupCtx)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(
			func(context.Context, *jetstream.Delivery) jetstream.HandlerResult {
				if err := store.upsert(rowKey, 118_250.5); err != nil {
					return jetstream.HandlerResult{Decision: jetstream.RETRY, Err: err}
				}
				// Model an ACK transport loss: the process context ends after Storage
				// commits but before Runner can send the ACK.
				cancelFirst()
				return jetstream.HandlerResult{Decision: jetstream.ACK}
			},
		), jetstream.RunnerConfig{BatchSize: 10, IndependentBatch: true}).Run(firstCtx)
	}()
	if err := waitRunner(t, firstDone); err != nil {
		t.Fatalf("first runner error = %v", err)
	}

	secondCtx, cancelSecond := context.WithCancel(setupCtx)
	recorder := &actionRecorder{actions: make(chan actionRecord, 2)}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(
			func(_ context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
				decoded := events.DecodeDelivery(registry, delivery)
				if decoded.Err != nil {
					return jetstream.HandlerResult{Decision: jetstream.TERM, Err: decoded.Err}
				}
				if err := store.upsert(rowKey, 118_250.5); err != nil {
					return jetstream.HandlerResult{Decision: jetstream.RETRY, Err: err}
				}
				return jetstream.HandlerResult{Decision: jetstream.ACK}
			},
		), jetstream.RunnerConfig{
			BatchSize:        10,
			IndependentBatch: true,
			ActionReporter: redeliveryActionReporterFunc(func(
				_ context.Context,
				delivery *jetstream.Delivery,
				result jetstream.HandlerResult,
				err error,
			) {
				recorder.ReportAction(context.Background(), delivery, result, err)
				if err == nil && result.Decision == jetstream.ACK {
					cancelSecond()
				}
			}),
		}).Run(secondCtx)
	}()

	action := waitBatchAction(t, recorder.actions)
	if action.id != itemID || action.decision != jetstream.ACK || action.deliveryCount != 2 || action.err != nil {
		t.Fatalf("redelivery action = %+v, want ACK delivery_count=2", action)
	}
	if err := waitRunner(t, secondDone); err != nil {
		t.Fatalf("second runner error = %v", err)
	}

	var count int64
	if err := store.db.Model(&storedKlineRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	var row storedKlineRow
	if err := store.db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1", count)
	}
	if row.Close != 118_250.5 {
		t.Fatalf("stored close = %v, want 118250.5", row.Close)
	}
	if got := store.upserts.Load(); got != 2 {
		t.Fatalf("Storage upsert calls = %d, want 2", got)
	}
	if row.RowKey == "" {
		t.Fatal(fmt.Errorf("stored canonical RowKey is empty"))
	}
}
