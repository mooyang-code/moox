package test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	eventconsumer "github.com/mooyang-code/moox/modules/archive/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/parquet-go/parquet-go"
)

func TestArchiveConsumesUpdatesAndMaterializesMonthlyParquet(t *testing.T) {
	storageSubject := "moox.event.storage.dataset.rows.upserted.v2.>"
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer ns.Shutdown()
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{storageSubject}, Storage: nats.FileStorage})
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{Name: "archive-e2e", Durable: "archive-e2e", FilterSubject: storageSubject, AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: -1})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store, err := journal.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := writer.New(store, root, 100)
	// This case owns an unauthenticated embedded NATS server; do not inherit
	// credentials/URL variables from the real deployment E2E wrapper.
	client, err := jetstream.Connect(context.Background(), jetstream.Config{URLs: []string{ns.ClientURL()}, Name: "archive-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	pull, err := events.NewConsumer(context.Background(), client, registry, events.ConsumerConfig{
		Name: "archive-e2e", Event: events.DatasetRowsUpserted, AckWait: time.Minute,
		MaxDeliver: 5, MaxAckPending: 256, FetchMaxWait: 100 * time.Millisecond,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pull.Close()
	h := eventconsumer.NewHandler(eventconsumer.NewDecoder(map[string][]string{"crypto": {"dataset_spot_kline_1h"}}), store, nil)
	runner := eventconsumer.NewRunner(pull, h, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()

	publish := func(id, dataTime, tag, close string) {
		event := &sharedpb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", SourceNodeId: "node-1", SourceStoreId: "store-1", SourceSequence: 1, WriteSource: "test", Rows: []*sharedpb.RowUpsert{{Key: &sharedpb.RowKey{SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", Kind: &sharedpb.RowKey_TimeSeries{TimeSeries: &sharedpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1h", DataTime: dataTime, SeriesTag: tag}}}, Fields: []*sharedpb.FieldValue{{FieldId: "close", Value: &sharedpb.TypedValue{Value: &sharedpb.TypedValue_DoubleValue{DoubleValue: parseFloat(t, close)}}}}}}}
		_, err := publisher.Publish(context.Background(), events.DatasetRowsUpserted, event, events.PublishOptions{EventID: id, OccurredAt: time.Now().UTC(), SpaceID: event.GetSpaceId(), SubjectID: event.GetDatasetId()})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Publish out of time order and patch one identity. Each venue must still
	// materialize one independently sorted, unique monthly Parquet v2 file.
	publish("e1", "2026-06-30T23:59:00Z", "venue:binance", "100")
	publish("e2", "2026-06-30T23:59:00Z", "venue:okx", "200")
	publish("e3", "2026-06-30T23:58:00Z", "venue:binance", "99")
	publish("e4", "2026-06-30T23:58:00Z", "venue:okx", "199")
	publish("e5", "2026-06-30T23:59:00Z", "venue:binance", "101")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.WriteDirty(context.Background(), 100); err == nil {
			june := domain.PartitionKey{SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
			okx := june
			okx.SeriesTag = "venue:okx"
			jp, _ := june.AbsolutePath(root)
			op, _ := okx.AbsolutePath(root)
			jr, _, _, je := parquetio.Read(jp)
			or, _, _, oe := parquetio.Read(op)
			if je == nil && oe == nil && len(jr) == 2 && len(or) == 2 &&
				*jr[1].Columns["close"].Double == 101 && *or[1].Columns["close"].Double == 200 {
				assertArchivePartitionIdentity(t, root, june, jp)
				assertArchivePartitionIdentity(t, root, okx, op)
				assertIndependentParquetRows(t, jp, "venue:binance", 2)
				assertIndependentParquetRows(t, op, "venue:okx", 2)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("archive files were not materialized")
}

func assertArchivePartitionIdentity(t *testing.T, _ string, key domain.PartitionKey, path string) {
	t.Helper()
	parsed, err := domain.ParseArchivePath(path)
	if err != nil || parsed != key {
		t.Fatalf("local archive path key=%+v err=%v, want %+v", parsed, err, key)
	}
	encodedTag := domain.EncodeIdentity(key.SeriesTag)
	decodedTag, err := domain.DecodeIdentity(encodedTag)
	if err != nil || decodedTag != key.SeriesTag {
		t.Fatalf("series tag encoding %q decoded=%q err=%v", encodedTag, decodedTag, err)
	}
	if !containsPathSegment(filepath.ToSlash(path), "series_tag="+encodedTag) {
		t.Fatalf("local path does not use canonical tag encoding: %q", path)
	}
}

func assertIndependentParquetRows(t *testing.T, path, wantTag string, wantRows int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	parquetFile, err := parquet.OpenFile(file, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	columns := parquetFile.Schema().Columns()
	tagIndex, timeIndex := -1, -1
	for i, column := range columns {
		switch strings.Join(column, ".") {
		case "series_tag":
			tagIndex = i
		case "candle_begin_time":
			timeIndex = i
		}
	}
	if tagIndex < 0 || timeIndex < 0 {
		t.Fatalf("%s missing v2 identity columns: %v", path, columns)
	}
	timeLeaf, ok := parquetFile.Schema().Lookup("candle_begin_time")
	if !ok || timeLeaf.Node.Type().LogicalType() == nil ||
		timeLeaf.Node.Type().LogicalType().Timestamp == nil ||
		timeLeaf.Node.Type().LogicalType().Timestamp.Unit.Nanos == nil {
		t.Fatalf("%s candle_begin_time is not a nanosecond timestamp", path)
	}
	reader := parquet.NewReader(file)
	defer reader.Close()
	rows := make([]parquet.Row, wantRows+1)
	n, readErr := reader.ReadRows(rows)
	if readErr != nil && readErr != io.EOF {
		t.Fatal(readErr)
	}
	if n != wantRows {
		t.Fatalf("%s rows=%d want=%d", path, n, wantRows)
	}
	times := make([]time.Time, 0, n)
	for _, row := range rows[:n] {
		var tag string
		var at time.Time
		row.Range(func(columnIndex int, values []parquet.Value) bool {
			if len(values) == 0 {
				return true
			}
			value := values[0]
			switch columnIndex {
			case tagIndex:
				tag = value.String()
			case timeIndex:
				at = time.Unix(0, value.Int64()).UTC()
			}
			return true
		})
		if tag != wantTag {
			t.Fatalf("%s series_tag=%q, want constant %q", path, tag, wantTag)
		}
		if at.IsZero() {
			t.Fatalf("%s missing candle_begin_time", path)
		}
		times = append(times, at)
	}
	if !sort.SliceIsSorted(times, func(i, j int) bool { return times[i].Before(times[j]) }) {
		t.Fatalf("%s candle_begin_time is not sorted: %v", path, times)
	}
	for i := 1; i < len(times); i++ {
		if times[i].Equal(times[i-1]) {
			t.Fatalf("%s contains duplicate candle_begin_time %s", path, times[i])
		}
	}
}

func containsPathSegment(path, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func parseFloat(t *testing.T, value string) float64 {
	var out float64
	if _, err := fmt.Sscan(value, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
