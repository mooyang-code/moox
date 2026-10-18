package command

import (
	"context"
	"errors"
	"os"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
)

const ()

type storageMetadataAPI interface {
	CreateSpace(context.Context, *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error)
	UpdateSpace(context.Context, *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error)
	DeleteSpace(context.Context, *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error)
	ListSpaces(context.Context, *storagepb.ListSpacesReq) (*storagepb.ListSpacesRsp, error)
	CreateDataSource(context.Context, *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error)
	UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error)
	DeleteDataSource(context.Context, *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error)
	RegisterDataNode(context.Context, *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error)
	UpdateDataNode(context.Context, *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error)
	RebindDatasetDataNode(context.Context, *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error)
	DeleteDataNode(context.Context, *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error)
	ListDataNodes(context.Context, *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type storagePrimaryAPI interface {
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error)
	ReadRows(context.Context, *storagepb.PrimaryReadRowsReq) (*storagepb.PrimaryReadRowsRsp, error)
}

type storageMetadataProxy struct{ gateway commandGateway }

func (c *storageMetadataProxy) CreateSpace(ctx context.Context, req *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error) {
	rsp := &storagepb.CreateSpaceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "CreateSpace", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) DeleteSpace(ctx context.Context, req *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error) {
	rsp := &storagepb.DeleteSpaceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteSpace", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) ListSpaces(ctx context.Context, req *storagepb.ListSpacesReq) (*storagepb.ListSpacesRsp, error) {
	rsp := &storagepb.ListSpacesRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "ListSpaces", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) UpdateSpace(ctx context.Context, req *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error) {
	rsp := &storagepb.UpdateSpaceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "UpdateSpace", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) CreateDataSource(ctx context.Context, req *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error) {
	rsp := &storagepb.CreateDataSourceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "CreateDataSource", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) UpdateDataSource(ctx context.Context, req *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error) {
	rsp := &storagepb.UpdateDataSourceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "UpdateDataSource", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) DeleteDataSource(ctx context.Context, req *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error) {
	rsp := &storagepb.DeleteDataSourceRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteDataSource", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	rsp := &storagepb.CreateDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "CreateDataset", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	rsp := &storagepb.GetDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "GetDataset", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	rsp := &storagepb.UpdateDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "UpdateDataset", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	rsp := &storagepb.DeleteDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteDataset", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	rsp := &storagepb.UpsertDatasetColumnRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "UpsertDatasetColumn", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	rsp := &storagepb.ListDatasetColumnsRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "ListDatasetColumns", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) RegisterDataNode(ctx context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	rsp := &storagepb.RegisterDataNodeRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "RegisterDataNode", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) UpdateDataNode(ctx context.Context, req *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error) {
	rsp := &storagepb.UpdateDataNodeRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "UpdateDataNode", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) RebindDatasetDataNode(ctx context.Context, req *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error) {
	rsp := &storagepb.RebindDatasetDataNodeRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "RebindDatasetDataNode", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) DeleteDataNode(ctx context.Context, req *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error) {
	rsp := &storagepb.DeleteDataNodeRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteDataNode", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) ListDataNodes(ctx context.Context, req *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error) {
	rsp := &storagepb.ListDataNodesRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "ListDataNodes", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	rsp := &storagepb.CheckDatasetActivationRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "CheckDatasetActivation", req, rsp)
	return rsp, err
}

func (c *storageMetadataProxy) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	rsp := &storagepb.ActivateDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "ActivateDataset", req, rsp)
	return rsp, err
}

type storagePrimaryProxy struct{ gateway commandGateway }

func (c *storagePrimaryProxy) UpsertFields(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	rsp := &storagepb.PrimaryUpsertFieldsRsp{}
	err := postStorageRaw(ctx, c.gateway, accessServiceName, "UpsertFields", req, rsp)
	return rsp, err
}

func (c *storagePrimaryProxy) ReadRows(ctx context.Context, req *storagepb.PrimaryReadRowsReq) (*storagepb.PrimaryReadRowsRsp, error) {
	rsp := &storagepb.PrimaryReadRowsRsp{}
	err := postStorageRaw(ctx, c.gateway, accessServiceName, "ReadRows", req, rsp)
	return rsp, err
}

type remoteStorageSession struct {
	transport   setupssh.Client
	gateway     commandGateway
	metadata    storageMetadataAPI
	primary     storagePrimaryAPI
	auth        *storagepb.AuthInfo
	nodeAuth    *storagepb.AuthInfo
	primaryAuth *storagepb.AuthInfo
}

func (s *remoteStorageSession) Close() {
	if s != nil && s.gateway != nil {
		_ = s.gateway.Close()
	}
}

func newRemoteStorageSession(transport setupssh.Client, gateway commandGateway, secret, primarySecret string) (*remoteStorageSession, error) {
	if transport == nil || gateway == nil || strings.TrimSpace(secret) == "" || strings.TrimSpace(primarySecret) == "" {
		return nil, errors.New("storage_verification_unavailable")
	}
	return &remoteStorageSession{
		transport: transport, gateway: gateway,
		metadata: &storageMetadataProxy{gateway: gateway}, primary: &storagePrimaryProxy{gateway: gateway},
		auth:        &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: security.HMACSHA256Hex(secret, []byte("storage-metadata"))},
		nodeAuth:    &storagepb.AuthInfo{AppId: "storage-deployer", AppKey: security.HMACSHA256Hex(secret, []byte("storage-deployer"))},
		primaryAuth: &storagepb.AuthInfo{AppId: "storage-e2e", AppKey: security.HMACSHA256Hex(primarySecret, []byte("storage-e2e"))},
	}, nil
}

func openRemoteStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) (setupconfig.Host, setupssh.Client, *remoteStorageSession, string, error) {
	if snapshot == nil {
		return setupconfig.Host{}, nil, nil, "", errors.New("storage_verification_invalid")
	}
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, name)
	if err != nil {
		return setupconfig.Host{}, nil, nil, "", err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return setupconfig.Host{}, nil, nil, "", err
	}
	secrets, err := readRemoteStorageRoleSecrets(ctx, transport, snapshot.Manifest.Paths.Resolved().StorageRoot)
	if err != nil {
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	session, err := newRemoteStorageSession(transport, gateway, secrets.Node, secrets.Primary)
	if err != nil {
		_ = gateway.Close()
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	root, err := os.Getwd()
	if err != nil {
		session.Close()
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", errors.New("storage_verification_invalid")
	}
	return host, transport, session, root, nil
}
