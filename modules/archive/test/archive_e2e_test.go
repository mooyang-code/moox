package test

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/cosstore"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	eventconsumer "github.com/mooyang-code/moox/modules/archive/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestArchiveConsumesUpdatesAndMaterializesMonthlyParquet(t *testing.T) {
	storageSubject := "moox.storage.dataset.rows.upserted.v2.>"
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
	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv([]string{ns.ClientURL()}, "archive-e2e"))
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
	h := eventconsumer.NewHandler(eventconsumer.NewDecoder(map[string][]string{"crypto": {"spot_kline_1h"}}), store, nil)
	runner := eventconsumer.NewRunner(pull, h, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()

	publish := func(id, dataTime, tag, close string) {
		event := &sharedpb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "spot_kline_1h", Rows: []*sharedpb.RowUpsert{{Key: &sharedpb.RowKey{SpaceId: "crypto", DatasetId: "spot_kline_1h", Kind: &sharedpb.RowKey_TimeSeries{TimeSeries: &sharedpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1h", DataTime: dataTime, SeriesTag: tag}}}, Fields: []*sharedpb.FieldValue{{FieldId: "close", Value: &sharedpb.TypedValue{Value: &sharedpb.TypedValue_DoubleValue{DoubleValue: parseFloat(t, close)}}}}}}}
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
			june := domain.PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
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

func TestDeployedArchiveConsumesRealStorageOutbox(t *testing.T) {
	if os.Getenv("MOOX_SERIES_TAG_E2E") != "1" {
		t.Skip("requires scripts/test-series-tag-e2e.sh")
	}
	archiveRoot := requiredArchiveEnv(t, "MOOX_ARCHIVE_E2E_ROOT")
	pidRaw, err := os.ReadFile(requiredArchiveEnv(t, "MOOX_ARCHIVE_E2E_PID_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid deployed Archive pid %q: %v", pidRaw, err)
	}
	archiveProcess, err := os.FindProcess(pid)
	if err != nil || archiveProcess.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("deployed Archive process %d is not running: %v", pid, err)
	}

	credentials := gatewayauth.CredentialsFromEnv()
	if credentials.KeyID == "" || credentials.Caller == "" || credentials.Secret == "" {
		t.Fatal("gateway credentials are required")
	}
	target := requiredArchiveEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET")
	nodeID := requiredArchiveEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")
	options := gatewayauth.NewTRPCClientOptions(target, nodeID, credentials)
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	metadata := storagepb.NewMetadataClientProxy(options...)
	metadataSetup := storagepb.NewMetadataClientProxy(
		client.WithTarget("ip://127.0.0.1:20100"),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
	)
	auth := &commonpb.AuthInfo{
		AppId: "moox-factor", Operator: "archive-storage-e2e",
		RequestId: fmt.Sprintf("archive-storage-e2e-%d", time.Now().UnixNano()),
	}
	auth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredArchiveEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET"),
		[]byte(auth.AppId),
	)
	deviceRsp, err := metadataSetup.CreateDevice(t.Context(), &storagepb.CreateDeviceReq{
		AuthInfo: auth,
		Device: &storagepb.Device{
			DeviceId: "parquet-local", Name: "Series Tag E2E Archive",
			Engine: "parquet", Endpoint: archiveRoot, Status: "active",
		},
	})
	requireArchiveRPCOK(t, "Metadata.CreateDevice", deviceRsp.GetRetInfo(), err)

	const (
		spaceID   = "crypto"
		datasetID = "spot_kline_1h"
		subjectID = "APT-USDT"
		freq      = "1h"
	)
	first := time.Date(2026, time.June, 20, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	tags := []string{"venue:binance", "venue:okx"}
	rows := make([]*storagepb.RowFieldUpsert, 0, 4)
	for tagIndex, tag := range tags {
		for timeIndex, at := range []time.Time{first, second} {
			base := float64(100 + 100*tagIndex + timeIndex)
			rows = append(rows, archiveStorageRow(spaceID, datasetID, subjectID, freq, tag, at, base))
		}
	}
	writeRsp, err := primary.UpsertFields(t.Context(), &storagepb.PrimaryUpsertFieldsReq{
		AuthInfo: auth, SourceEventId: fmt.Sprintf("archive-real-e2e-%d", time.Now().UnixNano()), Rows: rows,
	})
	requireArchiveRPCOK(t, "PrimaryStore.UpsertFields", writeRsp.GetRetInfo(), err)

	keys := make([]*storagepb.RowKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.GetKey())
	}
	require.Eventually(t, func() bool {
		readRsp, readErr := primary.ReadFields(t.Context(), &storagepb.PrimaryReadFieldsReq{
			AuthInfo: auth, Keys: keys, FieldIds: []string{"open", "high", "low", "close", "volume"},
		})
		if readErr != nil || readRsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return false
		}
		seen := map[string]bool{}
		for _, row := range readRsp.GetRows() {
			seen[row.GetKey().GetTimeSeries().GetSeriesTag()] = true
		}
		return seen[tags[0]] && seen[tags[1]]
	}, 10*time.Second, 100*time.Millisecond, "real Primary rows were not readable")

	// The deployed timer starts before these writes. Stopping the deployed
	// process exercises its real consumer drain and FlushOnShutdown path rather
	// than calling Archive internals from the test process.
	time.Sleep(3 * time.Second)
	if err := archiveProcess.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop deployed Archive for flush: %v", err)
	}
	require.Eventually(t, func() bool {
		return archiveProcess.Signal(syscall.Signal(0)) != nil
	}, 10*time.Second, 100*time.Millisecond, "deployed Archive did not exit after flush")

	month := first.Format("200601")
	partitions := []domain.PartitionKey{
		{SpaceID: spaceID, DatasetID: datasetID, SubjectID: subjectID, Freq: freq, SeriesTag: tags[0], Month: month},
		{SpaceID: spaceID, DatasetID: datasetID, SubjectID: subjectID, Freq: freq, SeriesTag: tags[1], Month: month},
	}
	require.Eventually(t, func() bool {
		for _, key := range partitions {
			path, pathErr := key.AbsolutePath(archiveRoot)
			if pathErr != nil {
				return false
			}
			if _, statErr := os.Stat(path); statErr != nil {
				return false
			}
		}
		return true
	}, 20*time.Second, 100*time.Millisecond, "deployed Archive did not flush both tag partitions")

	var filesByPartition map[string]*storagepb.ArchiveFile
	require.Eventually(t, func() bool {
		listRsp, listErr := metadata.ListArchiveFiles(t.Context(), &storagepb.ListArchiveFilesReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: 1, Size: 100},
		})
		if listErr != nil || listRsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return false
		}
		filesByPartition = make(map[string]*storagepb.ArchiveFile, len(listRsp.GetArchiveFiles()))
		for _, file := range listRsp.GetArchiveFiles() {
			filesByPartition[file.GetPartitionKey()] = file
		}
		return len(filesByPartition) >= len(partitions)
	}, 10*time.Second, 100*time.Millisecond, "Metadata registry did not expose both tag partitions")
	for _, key := range partitions {
		path, pathErr := key.AbsolutePath(archiveRoot)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		assertIndependentParquetRows(t, path, key.SeriesTag, 2)
		wantPartition := key.Freq + "/" + domain.EncodeIdentity(key.SubjectID) +
			"/series_tag=" + domain.EncodeIdentity(key.SeriesTag) + "/" + key.Month
		file := filesByPartition[wantPartition]
		if file == nil {
			t.Fatalf("Metadata registry has no deployed Archive partition %q: %v", wantPartition, filesByPartition)
		}
		fileURL, parseErr := url.Parse(file.GetFileUri())
		registryPath, registryPathErr := filepath.EvalSymlinks(fileURL.Path)
		localPath, localPathErr := filepath.EvalSymlinks(path)
		if parseErr != nil || fileURL.Scheme != "file" ||
			registryPathErr != nil || localPathErr != nil || registryPath != localPath {
			t.Fatalf("registry file_uri=%q, want file://%s: %v", file.GetFileUri(), path, parseErr)
		}
		if file.GetDeviceId() != "parquet-local" || file.GetAttributes()["schema_version"] != "2" {
			t.Fatalf("unexpected deployed Archive registry record: %+v", file)
		}
		if file.GetAttributes()["cos_status"] != "" || file.GetAttributes()["cos_object_key"] != "" {
			t.Fatalf("local-only Archive must not fabricate COS state: %+v", file.GetAttributes())
		}
		objectKey, objectKeyErr := cosstore.ObjectKey(archiveRoot, "moox/archive", path)
		relativePath, relativePathErr := key.RelativePath()
		wantObjectKey := "moox/archive/" + filepath.ToSlash(relativePath)
		if objectKeyErr != nil || relativePathErr != nil || objectKey != wantObjectKey {
			t.Fatalf("COS object key=%q, want %q: objectErr=%v relativeErr=%v", objectKey, wantObjectKey, objectKeyErr, relativePathErr)
		}
	}
}

func archiveStorageRow(space, dataset, subject, freq, tag string, at time.Time, base float64) *storagepb.RowFieldUpsert {
	double := func(name string, value float64) *storagepb.FieldValue {
		return &storagepb.FieldValue{
			FieldId: name,
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
		}
	}
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: space, DatasetId: dataset,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subject, Freq: freq, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: tag,
			}},
		},
		Fields: []*storagepb.FieldValue{
			double("open", base), double("high", base+2), double("low", base-2),
			double("close", base+1), double("volume", base*10),
		},
	}
}

func requireArchiveRPCOK(t *testing.T, action string, ret *commonpb.RetInfo, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	if ret == nil || ret.GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("%s: %+v", action, ret)
	}
}

func requiredArchiveEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("%s is required", name)
	}
	return value
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
