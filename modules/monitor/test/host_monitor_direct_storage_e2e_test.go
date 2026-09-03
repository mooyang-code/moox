//go:build integration

package test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

func TestHostMetricDirectStorageRoundTrip(t *testing.T) {
	if os.Getenv("MOOX_SERIES_TAG_E2E") != "1" {
		t.Fatal("integration test must be started through scripts/test/e2e/test-series-tag-e2e.sh")
	}
	credentials := gatewayauth.CredentialsFromEnv()
	if credentials.KeyID == "" || credentials.Caller == "" || credentials.Secret == "" {
		t.Fatal("gateway credentials are required")
	}
	target := normalizeGatewayTarget(requiredMonitorEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET"))
	nodeID := requiredMonitorEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")
	options := gatewayauth.NewTRPCClientOptions(target, nodeID, credentials)
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	_ = requiredMonitorEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET")

	cfg := monconfig.Default().Metrics.HostStorage
	cfg.KeyID = credentials.Caller
	writer := hostmetrics.NewStorageWriter(primary, cfg)
	observed := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	agentID := fmt.Sprintf("series-tag-e2e-%x", time.Now().UnixNano())
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
		Filesystems: []*hostmetricpb.FilesystemMetric{
			{Device: "/dev/sda1", Mountpoint: "/", FsType: "ext4", TotalBytes: 1000, UsedBytes: 500, AvailableBytes: 500, UsagePercent: 50},
			{Device: "/dev/sda1", Mountpoint: "/data|hot", FsType: "xfs", TotalBytes: 2000, UsedBytes: 500, AvailableBytes: 1500, UsagePercent: 25, ReadOnly: true},
		},
		Disks: []*hostmetricpb.DiskMetric{
			{
				Device: "sda", ReadBytesTotal: 10, WriteBytesTotal: 20,
				ReadOpsTotal: 3, WriteOpsTotal: 4, IoTimeMsTotal: 5,
				RateAvailable: true, ReadBytesPerSecond: 1.5, WriteBytesPerSecond: 2.5,
				ReadIops: 3.5, WriteIops: 4.5, UtilizationPercent: 10,
			},
			{
				Device: "disk:two", ReadBytesTotal: 30, WriteBytesTotal: 40,
				ReadOpsTotal: 6, WriteOpsTotal: 7, IoTimeMsTotal: 8,
			},
		},
		Networks: []*hostmetricpb.NetworkMetric{
			{
				Device: "eth0", Operstate: "up", ReceiveBytesTotal: 30, TransmitBytesTotal: 40,
				ReceiveErrorsTotal: 1, TransmitErrorsTotal: 2, ReceiveDroppedTotal: 3, TransmitDroppedTotal: 4,
				RateAvailable: true, ReceiveBytesPerSecond: 5.5, TransmitBytesPerSecond: 6.5,
				ErrorRateAvailable: true, ReceiveErrorsPerSecond: 0.5, TransmitErrorsPerSecond: 0.75,
			},
			{
				Device: "nic/two", Operstate: "down", ReceiveBytesTotal: 50, TransmitBytesTotal: 60,
				ReceiveErrorsTotal: 7, TransmitErrorsTotal: 8, ReceiveDroppedTotal: 9, TransmitDroppedTotal: 10,
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := writer.WriteSnapshot(ctx, snapshot, agentID, observed, "monitor-series-tag-e2e"); err != nil {
		t.Fatal(err)
	}
	auth := &commonpb.AuthInfo{AppId: cfg.KeyID}
	auth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredMonitorEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET"),
		[]byte(auth.AppId),
	)
	resourceKey := &storagepb.RowKey{
		SpaceId: cfg.SpaceID, DatasetId: cfg.ResourceDatasetID,
		Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: agentID, Freq: cfg.Frequency,
			DataTime: observed.Format(time.RFC3339Nano),
		}},
	}
	rawRsp, err := primary.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{
		AuthInfo: auth, Keys: []*storagepb.RowKey{resourceKey},
		FieldIds: []string{
			"agent_id", "logical_cores", "cpu_usage_percent", "cpu_usage_available",
			"memory_total_bytes", "memory_used_bytes", "memory_available_bytes", "memory_usage_percent",
		},
	})
	if err != nil || rawRsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		t.Fatalf("read raw host resource Primary row: rsp=%+v err=%v", rawRsp, err)
	}
	if len(rawRsp.GetRows()) != 1 {
		t.Fatalf("raw host resource Primary rows=%d, want 1", len(rawRsp.GetRows()))
	}
	assertRawHostResource(t, rawRsp.GetRows()[0].GetFields(), agentID, snapshot)

	reader := hostmetrics.NewStorageReader(primary, cfg)
	var points []hostmetrics.HistoryPoint
	var historyErr error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		points, historyErr = reader.History(ctx, agentID, observed.Add(-time.Minute), observed.Add(time.Minute), 10)
		if historyErr == nil && len(points) == 1 &&
			len(points[0].Snapshot.GetFilesystems()) == 2 &&
			len(points[0].Snapshot.GetDisks()) == 2 &&
			len(points[0].Snapshot.GetNetworks()) == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if historyErr != nil {
		t.Fatalf("read real Storage host history: %v", historyErr)
	}
	if len(points) != 1 {
		t.Fatalf("real Storage returned %d host points, want 1", len(points))
	}
	got := points[0].Snapshot
	assertHostSnapshot(t, got, snapshot)
}

func assertRawHostResource(t *testing.T, fields []*storagepb.FieldValue, agentID string, want *hostmetricpb.HostSnapshot) {
	t.Helper()
	values := make(map[string]*storagepb.TypedValue, len(fields))
	for _, field := range fields {
		name := strings.TrimPrefix(field.GetFieldId(), "dataset_mooxsys_host_resource.")
		values[name] = field.GetValue()
	}
	if len(values) != 8 ||
		values["agent_id"].GetStringValue() != agentID ||
		values["logical_cores"].GetIntValue() != int64(want.GetCpu().GetLogicalCores()) ||
		values["cpu_usage_percent"].GetDoubleValue() != want.GetCpu().GetUsagePercent() ||
		values["cpu_usage_available"].GetBoolValue() != want.GetCpu().GetUsageAvailable() ||
		values["memory_total_bytes"].GetIntValue() != int64(want.GetMemory().GetTotalBytes()) ||
		values["memory_used_bytes"].GetIntValue() != int64(want.GetMemory().GetUsedBytes()) ||
		values["memory_available_bytes"].GetIntValue() != int64(want.GetMemory().GetAvailableBytes()) ||
		values["memory_usage_percent"].GetDoubleValue() != want.GetMemory().GetUsagePercent() {
		t.Fatalf("raw host resource fields do not round-trip: %+v", values)
	}
}

func assertHostSnapshot(t *testing.T, got, want *hostmetricpb.HostSnapshot) {
	t.Helper()
	if got.GetCpu().GetLogicalCores() != want.GetCpu().GetLogicalCores() ||
		got.GetCpu().GetUsageAvailable() != want.GetCpu().GetUsageAvailable() ||
		got.GetCpu().GetUsagePercent() != want.GetCpu().GetUsagePercent() ||
		got.GetMemory().GetTotalBytes() != want.GetMemory().GetTotalBytes() ||
		got.GetMemory().GetUsedBytes() != want.GetMemory().GetUsedBytes() ||
		got.GetMemory().GetAvailableBytes() != want.GetMemory().GetAvailableBytes() ||
		got.GetMemory().GetUsagePercent() != want.GetMemory().GetUsagePercent() {
		t.Fatalf("resource metrics differ: got=%+v want=%+v", got, want)
	}
	if len(got.GetFilesystems()) != 2 || len(got.GetDisks()) != 2 || len(got.GetNetworks()) != 2 {
		t.Fatalf("same-minute entities were overwritten: %+v", got)
	}
	gotFS := map[string]*hostmetricpb.FilesystemMetric{}
	for _, item := range got.GetFilesystems() {
		gotFS[item.GetDevice()+"\x00"+item.GetMountpoint()] = item
	}
	for _, expected := range want.GetFilesystems() {
		actual := gotFS[expected.GetDevice()+"\x00"+expected.GetMountpoint()]
		if actual == nil ||
			actual.GetFsType() != expected.GetFsType() ||
			actual.GetTotalBytes() != expected.GetTotalBytes() ||
			actual.GetUsedBytes() != expected.GetUsedBytes() ||
			actual.GetAvailableBytes() != expected.GetAvailableBytes() ||
			actual.GetUsagePercent() != expected.GetUsagePercent() ||
			actual.GetReadOnly() != expected.GetReadOnly() {
			t.Fatalf("filesystem %s/%s differs: got=%+v want=%+v", expected.GetDevice(), expected.GetMountpoint(), actual, expected)
		}
	}
	gotDisks := map[string]*hostmetricpb.DiskMetric{}
	for _, item := range got.GetDisks() {
		gotDisks[item.GetDevice()] = item
	}
	for _, expected := range want.GetDisks() {
		actual := gotDisks[expected.GetDevice()]
		if actual == nil ||
			actual.GetReadBytesTotal() != expected.GetReadBytesTotal() ||
			actual.GetWriteBytesTotal() != expected.GetWriteBytesTotal() ||
			actual.GetReadOpsTotal() != expected.GetReadOpsTotal() ||
			actual.GetWriteOpsTotal() != expected.GetWriteOpsTotal() ||
			actual.GetIoTimeMsTotal() != expected.GetIoTimeMsTotal() ||
			actual.GetRateAvailable() != expected.GetRateAvailable() ||
			actual.GetReadBytesPerSecond() != expected.GetReadBytesPerSecond() ||
			actual.GetWriteBytesPerSecond() != expected.GetWriteBytesPerSecond() ||
			actual.GetReadIops() != expected.GetReadIops() ||
			actual.GetWriteIops() != expected.GetWriteIops() ||
			actual.GetUtilizationPercent() != expected.GetUtilizationPercent() {
			t.Fatalf("disk %s differs: got=%+v want=%+v", expected.GetDevice(), actual, expected)
		}
	}
	gotNetworks := map[string]*hostmetricpb.NetworkMetric{}
	for _, item := range got.GetNetworks() {
		gotNetworks[item.GetDevice()] = item
	}
	for _, expected := range want.GetNetworks() {
		actual := gotNetworks[expected.GetDevice()]
		if actual == nil ||
			actual.GetOperstate() != expected.GetOperstate() ||
			actual.GetReceiveBytesTotal() != expected.GetReceiveBytesTotal() ||
			actual.GetTransmitBytesTotal() != expected.GetTransmitBytesTotal() ||
			actual.GetReceiveErrorsTotal() != expected.GetReceiveErrorsTotal() ||
			actual.GetTransmitErrorsTotal() != expected.GetTransmitErrorsTotal() ||
			actual.GetReceiveDroppedTotal() != expected.GetReceiveDroppedTotal() ||
			actual.GetTransmitDroppedTotal() != expected.GetTransmitDroppedTotal() ||
			actual.GetRateAvailable() != expected.GetRateAvailable() ||
			actual.GetReceiveBytesPerSecond() != expected.GetReceiveBytesPerSecond() ||
			actual.GetTransmitBytesPerSecond() != expected.GetTransmitBytesPerSecond() ||
			actual.GetErrorRateAvailable() != expected.GetErrorRateAvailable() ||
			actual.GetReceiveErrorsPerSecond() != expected.GetReceiveErrorsPerSecond() ||
			actual.GetTransmitErrorsPerSecond() != expected.GetTransmitErrorsPerSecond() {
			t.Fatalf("network %s differs: got=%+v want=%+v", expected.GetDevice(), actual, expected)
		}
	}
}

func requiredMonitorEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func normalizeGatewayTarget(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if strings.Contains(raw, "://") {
		return raw
	}
	return "ip://" + raw
}
