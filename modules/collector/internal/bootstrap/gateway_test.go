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
	"github.com/mooyang-code/moox/modules/collector/internal/adminrpc"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
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
	adminpb.UnimplementedSecretMgr
	adminpb.UnimplementedActivityLog
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
		"trpc.moox.storage.Metadata":     {HostIds: []string{"control"}},
		"trpc.moox.storage.PrimaryStore": {HostIds: []string{"control"}},
		"trpc.moox.storage.DataView":     {HostIds: []string{"control"}},
		"trpc.moox.ops.SecretMgr":        {HostIds: []string{"control"}},
		"trpc.moox.admin.ActivityLog":    {HostIds: []string{"control"}},
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
	if service == "trpc.moox.admin.ActivityLog" && headers.Get("X-Space-Id") != "crypto" {
		return errors.New("activity log space metadata changed")
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

func (w *collectorGatewayWire) ListSubjects(ctx context.Context, request *storagepb.ListSubjectsReq) (*storagepb.ListSubjectsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ListSubjects", request); err != nil {
		return nil, err
	}
	return &storagepb.ListSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) CommitTimeSeriesBatch(ctx context.Context, request *storagepb.PrimaryCommitTimeSeriesBatchReq) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) UpsertFields(ctx context.Context, request *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "UpsertFields", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *collectorGatewayWire) ReadRows(ctx context.Context, request *storagepb.PrimaryReadRowsReq) (*storagepb.PrimaryReadRowsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReadRows", request); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryReadRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

// collectorDataViewWire 单独实现 DataView：它和 PrimaryStore 都有 CountTimeSeriesRows，放进同一个类型会有歧义。
type collectorDataViewWire struct {
	storagepb.UnimplementedDataView
	wire *collectorGatewayWire
}

func (w collectorDataViewWire) GetViewState(ctx context.Context, request *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.storage.DataView", "GetViewState", request); err != nil {
		return nil, err
	}
	return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
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
	directory := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.PrimaryStore": {"control"}, "trpc.moox.storage.DataView": {"control"}, "trpc.moox.ops.SecretMgr": {"control"}, "trpc.moox.admin.ActivityLog": {"control"}}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	wire := &collectorGatewayWire{credentials: credentials, directory: directory, nonces: map[string]bool{}, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	storagepb.RegisterDataViewService(svc, &collectorDataViewWire{wire: wire})
	adminpb.RegisterSecretMgrService(svc, wire)
	adminpb.RegisterActivityLogService(svc, wire)
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
	t.Run("GetTag", func(t *testing.T) {
		_, err := adapter.GetTag(t.Context(), &storagepb.GetTagReq{})
		require.NoError(t, err)
	})
	t.Run("GetDataset", func(t *testing.T) {
		_, err := adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{})
		require.NoError(t, err)
	})
	t.Run("CreateDataset", func(t *testing.T) {
		_, err := adapter.CreateDataset(t.Context(), &storagepb.CreateDatasetReq{})
		require.NoError(t, err)
	})
	t.Run("UpdateDataset", func(t *testing.T) {
		_, err := adapter.UpdateDataset(t.Context(), &storagepb.UpdateDatasetReq{})
		require.NoError(t, err)
	})
	t.Run("DeleteDataset", func(t *testing.T) {
		_, err := adapter.DeleteDataset(t.Context(), &storagepb.DeleteDatasetReq{})
		require.NoError(t, err)
	})
	t.Run("GetView", func(t *testing.T) {
		_, err := adapter.GetView(t.Context(), &storagepb.GetViewReq{})
		require.NoError(t, err)
	})
	t.Run("CreateView", func(t *testing.T) {
		_, err := adapter.CreateView(t.Context(), &storagepb.CreateViewReq{})
		require.NoError(t, err)
	})
	t.Run("DeleteView", func(t *testing.T) {
		_, err := adapter.DeleteView(t.Context(), &storagepb.DeleteViewReq{})
		require.NoError(t, err)
	})
	t.Run("UpsertDatasetColumn", func(t *testing.T) {
		_, err := adapter.UpsertDatasetColumn(t.Context(), &storagepb.UpsertDatasetColumnReq{})
		require.NoError(t, err)
	})
	t.Run("ResolveSubjects", func(t *testing.T) {
		_, err := adapter.ResolveSubjects(t.Context(), &storagepb.ResolveSubjectsReq{})
		require.NoError(t, err)
	})
	t.Run("ListDatasetColumns", func(t *testing.T) {
		_, err := adapter.ListDatasetColumns(t.Context(), &storagepb.ListDatasetColumnsReq{})
		require.NoError(t, err)
	})
	t.Run("ListTags", func(t *testing.T) {
		_, err := adapter.ListTags(t.Context(), &storagepb.ListTagsReq{})
		require.NoError(t, err)
	})
	t.Run("ApplyTagSnapshot", func(t *testing.T) {
		_, err := adapter.ApplyTagSnapshot(t.Context(), &storagepb.ApplyTagSnapshotReq{})
		require.Error(t, err)
	})
	t.Run("UpdateSubjectAttributes", func(t *testing.T) {
		_, err := adapter.UpdateSubjectAttributes(t.Context(), &storagepb.UpdateSubjectAttributesReq{})
		require.NoError(t, err)
	})
	t.Run("GetDataSource", func(t *testing.T) {
		_, err := adapter.GetDataSource(t.Context(), &storagepb.GetDataSourceReq{})
		require.NoError(t, err)
	})
	t.Run("ListSubjects", func(t *testing.T) {
		_, err := adapter.ListSubjects(t.Context(), &storagepb.ListSubjectsReq{})
		require.NoError(t, err)
	})
	t.Run("CommitTimeSeriesBatch", func(t *testing.T) {
		_, err := adapter.CommitTimeSeriesBatch(t.Context(), &storagepb.PrimaryCommitTimeSeriesBatchReq{})
		require.NoError(t, err)
	})
	t.Run("UpsertFields", func(t *testing.T) {
		_, err := adapter.UpsertFields(t.Context(), &storagepb.PrimaryUpsertFieldsReq{})
		require.NoError(t, err)
	})
	t.Run("ReadRows", func(t *testing.T) {
		_, err := adapter.ReadRows(t.Context(), &storagepb.PrimaryReadRowsReq{})
		require.NoError(t, err)
	})
	t.Run("GetViewState", func(t *testing.T) {
		_, err := adapter.GetViewState(t.Context(), &storagepb.GetViewStateReq{})
		require.NoError(t, err)
	})
	wire.mu.Lock()
	require.Equal(t, 2, wire.calls["GetDataset"])
	require.Equal(t, 1, wire.calls["ApplyTagSnapshot"])
	require.Len(t, wire.calls, 20)
	wire.mu.Unlock()
	// 云账号密钥经网关读 Admin 的专用方法 GetCloudCredential。
	credential, err := (&cloud.GatewayCredentials{Gateway: gateway}).Resolve(t.Context(), store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "secret-1"})
	require.NoError(t, err)
	require.Equal(t, cloud.Credential{AccountID: "account-a", SecretID: "AKID-fixture", SecretKey: "fixture-secret-value"}, credential)
	// 活动日志带上空间元数据。
	require.NoError(t, adminrpc.New(gateway).AppendActivityLogs(t.Context(), "crypto", []adminrpc.ActivityEntry{{SpaceID: "crypto", Source: "collector", Title: "t"}}))
	wire.mu.Lock()
	require.Equal(t, 1, wire.calls["GetCloudCredential"])
	require.Equal(t, 1, wire.calls["AppendActivityLogs"])
	wire.mu.Unlock()
	_, err = adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{}, client.WithTarget("ip://192.0.2.99:1"))
	require.Error(t, err)
	cache := filepath.Join(data, "gatewayclient", "directory.json")
	info, err := os.Stat(cache)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, runtime.Close())
	_, err = adapter.GetDataset(t.Context(), &storagepb.GetDatasetReq{})
	require.Error(t, err)
}

func (w *collectorGatewayWire) GetCloudCredential(ctx context.Context, req *adminpb.GetCloudCredentialReq) (*adminpb.GetCloudCredentialRsp, error) {
	if err := w.verify(ctx, "trpc.moox.ops.SecretMgr", "GetCloudCredential", req); err != nil {
		return nil, err
	}
	return &adminpb.GetCloudCredentialRsp{RetInfo: &adminpb.RetInfo{}, Credential: &adminpb.CloudCredential{SecretId: req.GetSecretId(), KeyId: "AKID-fixture", SecretValue: "fixture-secret-value"}}, nil
}

func (w *collectorGatewayWire) AppendActivityLogs(ctx context.Context, req *adminpb.AppendActivityLogsReq) (*adminpb.AppendActivityLogsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.ActivityLog", "AppendActivityLogs", req); err != nil {
		return nil, err
	}
	return &adminpb.AppendActivityLogsRsp{RetInfo: &adminpb.RetInfo{}}, nil
}
