package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type collectorGatewayWire struct {
	adminpb.UnimplementedCollectorPublishLease
	cloudnodepb.UnimplementedCloudNodeMgr
	storagepb.UnimplementedMetadata
	storagepb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	directory   servicecatalog.Directory
	mu          sync.Mutex
	nonces      map[string]bool
	calls       map[string]int
}

func (w *collectorGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	if req.GetCurrentVersion() == w.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: w.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: w.directory.Version, Services: map[string]*directorypb.ServiceHosts{
		"trpc.moox.storage.Metadata":            {HostIds: []string{"control"}},
		"trpc.moox.storage.PrimaryStore":        {HostIds: []string{"control"}},
		"trpc.moox.cloudnode.CloudNodeMgr":      {HostIds: []string{"control"}},
		"trpc.moox.admin.CollectorPublishLease": {HostIds: []string{"control"}},
	}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}

func (w *collectorGatewayWire) verify(ctx context.Context, service, method string, request proto.Message) error {
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: "control", Callee: service, Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return err
	}
	if service == "trpc.moox.cloudnode.CloudNodeMgr" || service == "trpc.moox.admin.CollectorPublishLease" {
		if headers.Get("X-Space-Id") != "crypto" {
			return errors.New("CloudNode space metadata changed")
		}
	} else {
		role, ok := request.(interface{ GetAuthInfo() *storagepb.AuthInfo })
		if !ok || role.GetAuthInfo().GetAppId() != "moox-collector" || role.GetAuthInfo().GetAppKey() != "independent-storage-role-key" {
			return errors.New("collector Storage role authentication changed")
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nonces[claims.Nonce] {
		return errors.New("collector call reused a signing nonce")
	}
	w.nonces[claims.Nonce] = true
	w.calls[method]++
	if method == "GetDataset" && w.calls[method] == 1 || method == "ApplyTagSnapshot" {
		return errs.NewFrameError(errs.RetServerNoService, "refresh read; never retry write")
	}
	return nil
}

func (w *collectorGatewayWire) GetTag(ctx context.Context, request *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetTag", request); err != nil {
		return nil, err
	}
	return &storagepb.GetTagRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) GetDataset(ctx context.Context, request *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetDataset", request); err != nil {
		return nil, err
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) CreateDataset(ctx context.Context, request *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "CreateDataset", request); err != nil {
		return nil, err
	}
	return &storagepb.CreateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpdateDataset(ctx context.Context, request *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpdateDataset", request); err != nil {
		return nil, err
	}
	return &storagepb.UpdateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) DeleteDataset(ctx context.Context, request *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "DeleteDataset", request); err != nil {
		return nil, err
	}
	return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) GetView(ctx context.Context, request *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetView", request); err != nil {
		return nil, err
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) CreateView(ctx context.Context, request *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "CreateView", request); err != nil {
		return nil, err
	}
	return &storagepb.CreateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpdateView(ctx context.Context, request *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpdateView", request); err != nil {
		return nil, err
	}
	return &storagepb.UpdateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) DeleteView(ctx context.Context, request *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "DeleteView", request); err != nil {
		return nil, err
	}
	return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpsertDatasetColumn(ctx context.Context, request *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpsertDatasetColumn", request); err != nil {
		return nil, err
	}
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpsertViewColumn(ctx context.Context, request *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpsertViewColumn", request); err != nil {
		return nil, err
	}
	return &storagepb.UpsertViewColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) CheckDatasetActivation(ctx context.Context, request *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "CheckDatasetActivation", request); err != nil {
		return nil, err
	}
	return &storagepb.CheckDatasetActivationRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ActivateDataset(ctx context.Context, request *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ActivateDataset", request); err != nil {
		return nil, err
	}
	return &storagepb.ActivateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ResolveSubjects(ctx context.Context, request *storagepb.ResolveSubjectsReq) (*storagepb.ResolveSubjectsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ResolveSubjects", request); err != nil {
		return nil, err
	}
	return &storagepb.ResolveSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ListDatasetColumns(ctx context.Context, request *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ListDatasetColumns", request); err != nil {
		return nil, err
	}
	return &storagepb.ListDatasetColumnsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ListTags(ctx context.Context, request *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ListTags", request); err != nil {
		return nil, err
	}
	return &storagepb.ListTagsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ApplyTagSnapshot(ctx context.Context, request *storagepb.ApplyTagSnapshotReq) (*storagepb.ApplyTagSnapshotRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ApplyTagSnapshot", request); err != nil {
		return nil, err
	}
	return &storagepb.ApplyTagSnapshotRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ReportTagRunFailure(ctx context.Context, request *storagepb.ReportTagRunFailureReq) (*storagepb.ReportTagRunFailureRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ReportTagRunFailure", request); err != nil {
		return nil, err
	}
	return &storagepb.ReportTagRunFailureRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpdateSubjectAttributes(ctx context.Context, request *storagepb.UpdateSubjectAttributesReq) (*storagepb.UpdateSubjectAttributesRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpdateSubjectAttributes", request); err != nil {
		return nil, err
	}
	return &storagepb.UpdateSubjectAttributesRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) GetDataSource(ctx context.Context, request *storagepb.GetDataSourceReq) (*storagepb.GetDataSourceRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetDataSource", request); err != nil {
		return nil, err
	}
	return &storagepb.GetDataSourceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpdateDataSource(ctx context.Context, request *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpdateDataSource", request); err != nil {
		return nil, err
	}
	return &storagepb.UpdateDataSourceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ListSubjects(ctx context.Context, request *storagepb.ListSubjectsReq) (*storagepb.ListSubjectsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ListSubjects", request); err != nil {
		return nil, err
	}
	return &storagepb.ListSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) EnsureDatasetPeriod(ctx context.Context, request *storagepb.PrimaryEnsureDatasetPeriodReq) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryEnsureDatasetPeriodRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) GetDatasetPeriodStatus(ctx context.Context, request *storagepb.PrimaryGetDatasetPeriodStatusReq) (*storagepb.PrimaryGetDatasetPeriodStatusRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "GetDatasetPeriodStatus", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryGetDatasetPeriodStatusRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) CommitTimeSeriesBatch(ctx context.Context, request *storagepb.PrimaryCommitTimeSeriesBatchReq) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) RecordDatasetPeriodFailures(ctx context.Context, request *storagepb.PrimaryRecordDatasetPeriodFailuresReq) (*storagepb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "RecordDatasetPeriodFailures", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpsertFields(ctx context.Context, request *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "UpsertFields", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ReportCollectorPeriodCompleted(ctx context.Context, request *storagepb.ReportCollectorPeriodCompletedReq) (*storagepb.ReportCollectorPeriodCompletedRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReportCollectorPeriodCompleted", request); err != nil {
		return nil, err
	}
	return &storagepb.ReportCollectorPeriodCompletedRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ReadFields(ctx context.Context, request *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReadFields", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryReadFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) WaitViewSyncPoint(ctx context.Context, request *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "WaitViewSyncPoint", request); err != nil {
		return nil, err
	}
	return &storagepb.WaitViewSyncPointRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) DeleteDatasetRows(ctx context.Context, request *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "DeleteDatasetRows", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryDeleteDatasetRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) RestoreDatasetRows(ctx context.Context, request *storagepb.PrimaryRestoreDatasetRowsReq) (*storagepb.PrimaryRestoreDatasetRowsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "RestoreDatasetRows", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryRestoreDatasetRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestCollectorGatewayUsesDeploymentIdentityForAllStorageCapabilities(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "collector", "config", "app.yaml")
	hostPath := filepath.Join(root, "host-gateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-collector.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	}
	credentials := gatewayauth.Credentials{Caller: "collector", KeyID: "admin-assigned-collector-key-42", Secret: "collector-fixture-signing-key-at-least-32-bytes"}
	require.NoError(t, os.WriteFile(keyPath, []byte(credentials.Secret+"\n"), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host := hostgatewayconfig.Default("control", "control", "192.0.2.1", "host-fixture-key")
	host.Server.LocalAddr = listener.Addr().String()
	host.TLS.CAFile = caPath
	encoded, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostPath, encoded, 0600))
	directory := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.PrimaryStore": {"control"}, "trpc.moox.cloudnode.CloudNodeMgr": {"control"}, "trpc.moox.admin.CollectorPublishLease": {"control"}}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	wire := &collectorGatewayWire{credentials: credentials, directory: directory, nonces: map[string]bool{}, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	cloudnodepb.RegisterCloudNodeMgrService(svc, wire)
	adminpb.RegisterCollectorPublishLeaseService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "collector")
	raw := "database:\n  path: " + filepath.Join(data, "collector.db") + "\ngateway_client:\n  caller: collector\n  key_id: " + credentials.KeyID + "\n  key_file: ../../secrets/caller-collector.key\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0600))
	cfg, err := Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	runtime := newRuntime(context.Background(), nil)
	runtime.gateway = gateway
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	adapter := storageio.NewGatewayClient(gateway)
	auth := &storagepb.AuthInfo{AppId: "moox-collector", AppKey: "independent-storage-role-key"}
	t.Run("GetTag", func(t *testing.T) {
		_, err := adapter.GetTag(t.Context(), &storagepb.GetTagReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("GetDataset", func(t *testing.T) {
		_, err := adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("CreateDataset", func(t *testing.T) {
		_, err := adapter.CreateDataset(t.Context(), &storagepb.CreateDatasetReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpdateDataset", func(t *testing.T) {
		_, err := adapter.UpdateDataset(t.Context(), &storagepb.UpdateDatasetReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("DeleteDataset", func(t *testing.T) {
		_, err := adapter.DeleteDataset(t.Context(), &storagepb.DeleteDatasetReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("GetView", func(t *testing.T) {
		_, err := adapter.GetView(t.Context(), &storagepb.GetViewReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("CreateView", func(t *testing.T) {
		_, err := adapter.CreateView(t.Context(), &storagepb.CreateViewReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpdateView", func(t *testing.T) {
		_, err := adapter.UpdateView(t.Context(), &storagepb.UpdateViewReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("DeleteView", func(t *testing.T) {
		_, err := adapter.DeleteView(t.Context(), &storagepb.DeleteViewReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpsertDatasetColumn", func(t *testing.T) {
		_, err := adapter.UpsertDatasetColumn(t.Context(), &storagepb.UpsertDatasetColumnReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpsertViewColumn", func(t *testing.T) {
		_, err := adapter.UpsertViewColumn(t.Context(), &storagepb.UpsertViewColumnReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("CheckDatasetActivation", func(t *testing.T) {
		_, err := adapter.CheckDatasetActivation(t.Context(), &storagepb.CheckDatasetActivationReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ActivateDataset", func(t *testing.T) {
		_, err := adapter.ActivateDataset(t.Context(), &storagepb.ActivateDatasetReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ResolveSubjects", func(t *testing.T) {
		_, err := adapter.ResolveSubjects(t.Context(), &storagepb.ResolveSubjectsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ListDatasetColumns", func(t *testing.T) {
		_, err := adapter.ListDatasetColumns(t.Context(), &storagepb.ListDatasetColumnsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ListTags", func(t *testing.T) {
		_, err := adapter.ListTags(t.Context(), &storagepb.ListTagsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ApplyTagSnapshot", func(t *testing.T) {
		_, err := adapter.ApplyTagSnapshot(t.Context(), &storagepb.ApplyTagSnapshotReq{AuthInfo: auth})
		require.Error(t, err)
	})
	t.Run("ReportTagRunFailure", func(t *testing.T) {
		_, err := adapter.ReportTagRunFailure(t.Context(), &storagepb.ReportTagRunFailureReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpdateSubjectAttributes", func(t *testing.T) {
		_, err := adapter.UpdateSubjectAttributes(t.Context(), &storagepb.UpdateSubjectAttributesReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("GetDataSource", func(t *testing.T) {
		_, err := adapter.GetDataSource(t.Context(), &storagepb.GetDataSourceReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpdateDataSource", func(t *testing.T) {
		_, err := adapter.UpdateDataSource(t.Context(), &storagepb.UpdateDataSourceReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ListSubjects", func(t *testing.T) {
		_, err := adapter.ListSubjects(t.Context(), &storagepb.ListSubjectsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("EnsureDatasetPeriod", func(t *testing.T) {
		_, err := adapter.EnsureDatasetPeriod(t.Context(), &storagepb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("GetDatasetPeriodStatus", func(t *testing.T) {
		_, err := adapter.GetDatasetPeriodStatus(t.Context(), &storagepb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("CommitTimeSeriesBatch", func(t *testing.T) {
		_, err := adapter.CommitTimeSeriesBatch(t.Context(), &storagepb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("RecordDatasetPeriodFailures", func(t *testing.T) {
		_, err := adapter.RecordDatasetPeriodFailures(t.Context(), &storagepb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("UpsertFields", func(t *testing.T) {
		_, err := adapter.UpsertFields(t.Context(), &storagepb.PrimaryUpsertFieldsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ReportCollectorPeriodCompleted", func(t *testing.T) {
		_, err := adapter.ReportCollectorPeriodCompleted(t.Context(), &storagepb.ReportCollectorPeriodCompletedReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("ReadFields", func(t *testing.T) {
		_, err := adapter.ReadFields(t.Context(), &storagepb.PrimaryReadFieldsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("WaitViewSyncPoint", func(t *testing.T) {
		_, err := adapter.WaitViewSyncPoint(t.Context(), &storagepb.WaitViewSyncPointReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("DeleteDatasetRows", func(t *testing.T) {
		_, err := adapter.DeleteDatasetRows(t.Context(), &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	t.Run("RestoreDatasetRows", func(t *testing.T) {
		_, err := adapter.RestoreDatasetRows(t.Context(), &storagepb.PrimaryRestoreDatasetRowsReq{AuthInfo: auth})
		require.NoError(t, err)
	})
	wire.mu.Lock()
	require.Equal(t, 2, wire.calls["GetDataset"])
	require.Equal(t, 1, wire.calls["ApplyTagSnapshot"])
	require.Len(t, wire.calls, 32)
	wire.mu.Unlock()
	invoker := scfinvoker.New(scfinvoker.Config{Gateway: gateway})
	lease, err := invoker.AcquireCollectorPublishLease(t.Context(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 17, lease.FencingToken)
	lease, err = invoker.RenewCollectorPublishLease(t.Context(), lease)
	require.NoError(t, err)
	require.NoError(t, invoker.ReleaseCollectorPublishLease(t.Context(), lease))
	wire.mu.Lock()
	for _, method := range []string{"AcquireCollectorPublishLease", "RenewCollectorPublishLease", "ReleaseCollectorPublishLease"} {
		require.Equal(t, 1, wire.calls[method])
	}
	wire.mu.Unlock()
	nodes, err := invoker.ListMarketFetchers(t.Context(), "crypto")
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	result, err := invoker.Invoke(t.Context(), "crypto", "node-1", map[string]any{"action": "probe"}, cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_REQUEST_RESPONSE)
	require.NoError(t, err)
	require.Equal(t, "request-1", result.RequestID)
	job, err := invoker.GetRuntimeConfigBatchStatus(t.Context(), "crypto", "job-1")
	require.NoError(t, err)
	require.Equal(t, "job-1", job.GetJobId())
	_, err = invoker.SubmitRuntimeConfigs(t.Context(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "node-1"}})
	require.ErrorIs(t, err, scfinvoker.ErrRuntimeConfigSubmissionUnknown)
	wire.mu.Lock()
	cloudCalls := wire.calls["SubmitUpdateNodeRuntimeConfigs"]
	wire.mu.Unlock()
	require.Equal(t, 1, cloudCalls, "runtime config writes are sent once")
	_, err = adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{AuthInfo: auth}, client.WithTarget("ip://192.0.2.99:1"))
	require.Error(t, err)
	cache := filepath.Join(data, "gatewayclient", "directory.json")
	info, err := os.Stat(cache)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, runtime.Close())
	_, err = adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{AuthInfo: auth})
	require.Error(t, err)
}

func (w *collectorGatewayWire) GetNodeList(ctx context.Context, req *cloudnodepb.GetNodeListReq) (*cloudnodepb.GetNodeListRsp, error) {
	if err := w.verify(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeList", req); err != nil {
		return nil, err
	}
	return &cloudnodepb.GetNodeListRsp{Items: []*cloudnodepb.CloudNode{{NodeId: "node-1", PackageId: "package-1", DeploymentId: "deployment-1"}}}, nil
}
func (w *collectorGatewayWire) InvokeFunction(ctx context.Context, req *cloudnodepb.InvokeFunctionReq) (*cloudnodepb.InvokeFunctionRsp, error) {
	if err := w.verify(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "InvokeFunction", req); err != nil {
		return nil, err
	}
	return &cloudnodepb.InvokeFunctionRsp{Scf: &cloudnodepb.ScfInvokeResult{RequestId: "request-1"}}, nil
}
func (w *collectorGatewayWire) GetNodeBatchChange(ctx context.Context, req *cloudnodepb.GetNodeBatchChangeReq) (*cloudnodepb.GetNodeBatchChangeRsp, error) {
	if err := w.verify(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeBatchChange", req); err != nil {
		return nil, err
	}
	return &cloudnodepb.GetNodeBatchChangeRsp{Job: &cloudnodepb.NodeBatchSummary{JobId: req.GetJobId()}}, nil
}
func (w *collectorGatewayWire) SubmitUpdateNodeRuntimeConfigs(ctx context.Context, req *cloudnodepb.BatchUpdateNodeRuntimeConfigsReq) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	if err := w.verify(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitUpdateNodeRuntimeConfigs", req); err != nil {
		return nil, err
	}
	return nil, errs.NewFrameError(errs.RetServerSystemErr, "lost response after accepting write")
}

func (w *collectorGatewayWire) AcquireCollectorPublishLease(ctx context.Context, req *adminpb.AcquireCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: "lease-1", FencingToken: 17, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
}

func (w *collectorGatewayWire) RenewCollectorPublishLease(ctx context.Context, req *adminpb.RenewCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: req.GetLeaseId(), FencingToken: req.GetFencingToken(), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
}

func (w *collectorGatewayWire) ReleaseCollectorPublishLease(ctx context.Context, req *adminpb.ReleaseCollectorPublishLeaseReq) (*adminpb.ReleaseCollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "ReleaseCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Released: true}, nil
}
