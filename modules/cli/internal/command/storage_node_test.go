package command

import (
	"bytes"
	"context"
	"io"
	"testing"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStorageNodeAPI struct {
	updated []*storagepb.UpdateDataNodeReq
	deleted []*storagepb.DeleteDataNodeReq
	code    commonpb.ErrorCode
	closed  bool
}

func (f *fakeStorageNodeAPI) UpdateDataNode(_ context.Context, req *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error) {
	f.updated = append(f.updated, req)
	return &storagepb.UpdateDataNodeRsp{RetInfo: &commonpb.RetInfo{Code: f.code, Msg: "refused"}, Node: &storagepb.DataNode{Name: req.GetName(), Status: req.GetStatus()}}, nil
}

func (f *fakeStorageNodeAPI) DeleteDataNode(_ context.Context, req *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error) {
	f.deleted = append(f.deleted, req)
	return &storagepb.DeleteDataNodeRsp{RetInfo: &commonpb.RetInfo{Code: f.code, Msg: "refused"}}, nil
}

func (f *fakeStorageNodeAPI) Close() error { f.closed = true; return nil }

func useStorageNode(t *testing.T, fake *fakeStorageNodeAPI) {
	t.Helper()
	previous := openStorageNode
	openStorageNode = func(context.Context, string) (storageNodeAPI, io.Closer, error) { return fake, fake, nil }
	t.Cleanup(func() { openStorageNode = previous })
}

func runStorageNode(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newStorageNodeCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestStorageNodeUpdateSendsNameAndStatus(t *testing.T) {
	fake := &fakeStorageNodeAPI{}
	useStorageNode(t, fake)
	out, err := runStorageNode(t, "update", "--node-id", "storage-node-0", "--name", "主存储", "--status", "active")
	require.NoError(t, err)
	require.Len(t, fake.updated, 1)
	assert.Equal(t, "storage-node-0", fake.updated[0].GetNodeId())
	assert.Equal(t, "主存储", fake.updated[0].GetName())
	assert.Equal(t, "active", fake.updated[0].GetStatus())
	assert.Contains(t, out, `"status":"updated"`)
	assert.True(t, fake.closed)
}

func TestStorageNodeUpdateRequiresNodeAndAChange(t *testing.T) {
	fake := &fakeStorageNodeAPI{}
	useStorageNode(t, fake)
	_, err := runStorageNode(t, "update", "--name", "x")
	require.ErrorContains(t, err, "--node-id")
	_, err = runStorageNode(t, "update", "--node-id", "n")
	require.ErrorContains(t, err, "--name 或 --status")
	assert.Empty(t, fake.updated)
}

func TestStorageNodeDeleteNeedsConfirmationAndReportsRefusal(t *testing.T) {
	fake := &fakeStorageNodeAPI{}
	useStorageNode(t, fake)
	_, err := runStorageNode(t, "delete", "--node-id", "storage-node-0")
	require.ErrorContains(t, err, "--yes")
	assert.Empty(t, fake.deleted, "没有 --yes 不执行")

	out, err := runStorageNode(t, "delete", "--node-id", "storage-node-0", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, `"status":"deleted"`)
	require.Len(t, fake.deleted, 1)

	fake.code = commonpb.ErrorCode_INVALID_PARAM
	_, err = runStorageNode(t, "delete", "--node-id", "storage-node-0", "--yes")
	require.ErrorContains(t, err, "refused")
}

func TestArchiveWorkerRetireRequiresWorkerAndConfirmation(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.RetireArchiveWorkerRsp{RetInfo: ok(), Retired: true}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	require.ErrorContains(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{Yes: true}), "--worker")
	require.ErrorContains(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{WorkerID: "factor-old"}), "--yes")
	require.NoError(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{WorkerID: " archive-worker@old ", Yes: true}))
	assert.Equal(t, "RetireArchiveWorker", g.method)
	assert.Contains(t, out.String(), `"retired": true`)
}
