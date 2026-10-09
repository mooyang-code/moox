package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

const storageOperationsSecret = "storage-operations-fixture-secret"

type storageOperationsWire struct {
	pb.UnimplementedMetadata
	pb.UnimplementedPrimaryStore
	creates atomic.Int32
	writes  atomic.Int32
	markers atomic.Int32
}

func (w *storageOperationsWire) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	if req.GetSpace().GetSpaceId() != "crypto" || string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]) != "crypto" {
		return nil, fmt.Errorf("missing trusted space")
	}
	w.creates.Add(1)
	return &pb.CreateSpaceRsp{RetInfo: &pb.RetInfo{}}, nil
}

func (*storageOperationsWire) GetDataset(context.Context, *pb.GetDatasetReq) (*pb.GetDatasetRsp, error) {
	return &pb.GetDatasetRsp{RetInfo: &pb.RetInfo{}, Dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "prices", Freq: "1m"}}, nil
}

func (*storageOperationsWire) GetSubject(context.Context, *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	return &pb.GetSubjectRsp{RetInfo: &pb.RetInfo{}, Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "BTC-USDT", Status: "active"}}, nil
}

func (*storageOperationsWire) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: &pb.RetInfo{}, Columns: []*pb.DatasetColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}}}, nil
}

func (*storageOperationsWire) ListDatasetSubjects(context.Context, *pb.ListDatasetSubjectsReq) (*pb.ListDatasetSubjectsRsp, error) {
	return &pb.ListDatasetSubjectsRsp{RetInfo: &pb.RetInfo{}, DatasetSubjects: []*pb.DatasetSubject{{SubjectId: "BTC-USDT", Status: "active"}}}, nil
}

func storageOperationAuth(ctx context.Context, auth *pb.AuthInfo, appID string) error {
	if auth.GetAppId() != appID || auth.GetAppKey() != security.HMACSHA256Hex(storageOperationsSecret, []byte(appID)) || string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]) != "crypto" {
		return fmt.Errorf("invalid Storage role or trusted space")
	}
	return nil
}

func (*storageOperationsWire) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if err := storageOperationAuth(ctx, req.GetAuthInfo(), "moox-cli-data-export"); err != nil {
		return nil, err
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{}, Complete: true, Rows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "prices", SubjectId: "BTC-USDT"}}}}, nil
}

func (w *storageOperationsWire) UpsertFields(ctx context.Context, req *pb.PrimaryUpsertFieldsReq) (*pb.PrimaryUpsertFieldsRsp, error) {
	if err := storageOperationAuth(ctx, req.GetAuthInfo(), "moox-cli-data-import"); err != nil {
		return nil, err
	}
	if len(req.GetRows()) != 1 || req.GetRows()[0].GetKey().GetSpaceId() != "crypto" || req.GetRows()[0].GetKey().GetDatasetId() != "prices" {
		return nil, fmt.Errorf("unexpected import rows")
	}
	w.writes.Add(1)
	return &pb.PrimaryUpsertFieldsRsp{RetInfo: &pb.RetInfo{}}, nil
}

func (w *storageOperationsWire) AppendDatasetSyncPoint(ctx context.Context, req *pb.AppendDatasetSyncPointReq) (*pb.AppendDatasetSyncPointRsp, error) {
	if err := storageOperationAuth(ctx, req.GetAuthInfo(), "moox-cli-data-import"); err != nil {
		return nil, err
	}
	if req.GetSyncPoint().GetRequestId() == "" || req.GetSyncPoint().GetDatasetId() != "prices" {
		return nil, fmt.Errorf("missing import fence")
	}
	w.markers.Add(1)
	return &pb.AppendDatasetSyncPointRsp{RetInfo: &pb.RetInfo{}}, nil
}

func TestStorageOperationsUseSSHNativeGatewayAndIndependentRoleAuth(t *testing.T) {
	wire := &storageOperationsWire{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTimeout(time.Minute))
	pb.RegisterMetadataService(service, wire)
	pb.RegisterPrimaryStoreService(service, wire)
	serveDone := make(chan error, 1)
	go func() { serveDone <- service.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, service.Close(nil))
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			t.Error("Storage operation fixture did not stop")
		}
	})
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	process := startGatewayHelperProcess(t, buildGatewayE2EHelper(t), "--mode", "storage-native", "--node-id", klineGatewayNode, "--upstream-addr", listener.Addr().String(), "--ready-file", ready, "--nonce-dir", filepath.Join(dir, "nonces"), "--key-id", klineGatewayKeyID)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("Storage gateway required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(ready, 30*time.Second)
	require.NoError(t, err)
	home, manifest := writeKlineOperator(t, target)
	binary := buildMooxCLI(t)
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "HOME="+home, "MOOX_STORAGE_PRIMARY_AUTH_SECRET="+storageOperationsSecret)
		var diagnostics bytes.Buffer
		command.Stderr = &diagnostics
		out, err := command.Output()
		require.NoError(t, err, "CLI diagnostics: %s", diagnostics.String())
		require.NotContains(t, string(out)+diagnostics.String(), storageOperationsSecret)
		require.NotContains(t, string(out)+diagnostics.String(), klineGatewaySecret)
		return out
	}
	seed := filepath.Join(dir, "seed.yaml")
	require.NoError(t, os.WriteFile(seed, []byte("spaces:\n- space_id: crypto\n  name: Crypto\n"), 0600))
	var imported map[string]any
	require.NoError(t, json.Unmarshal(run("metadata", "import", "--file", seed, "--manifest", manifest), &imported))
	require.Equal(t, float64(1), imported["applied"])
	var exported map[string]any
	require.NoError(t, json.Unmarshal(run("data", "rows", "export", "--file", manifest, "--space", "crypto", "--dataset", "prices", "--subject", "BTC-USDT"), &exported))
	require.Len(t, exported["rows"], 1)
	csv := filepath.Join(dir, "prices.csv")
	require.NoError(t, os.WriteFile(csv, []byte("time,close\n2026-10-09T00:00:00Z,12.5\n"), 0600))
	var written map[string]any
	require.NoError(t, json.Unmarshal(run("storage", "import", "--file", csv, "--manifest", manifest, "--space", "crypto", "--dataset", "prices", "--subject", "BTC-USDT", "--freq", "1m", "--time-column", "time"), &written))
	require.Equal(t, float64(1), written["written_rows"])
	require.Equal(t, int32(1), wire.creates.Load())
	require.Equal(t, int32(1), wire.writes.Load())
	require.Equal(t, int32(1), wire.markers.Load())
}
