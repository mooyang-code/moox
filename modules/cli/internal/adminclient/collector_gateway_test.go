package adminclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type collectorWire struct {
	pb.UnimplementedCollectMgr
	directorypb.UnimplementedDirectory
	calls chan string
}

func (w *collectorWire) GetDirectory(_ context.Context, _ *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	d := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "public-must-not-be-dialed.example.test"}}, Services: map[string][]string{"trpc.moox.collector.CollectMgr": {"control"}}}
	version, err := d.VersionHash()
	return &directorypb.GetDirectoryRsp{Changed: true, Version: version, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: d.Hosts["control"].Address}}, Services: map[string]*directorypb.ServiceHosts{"trpc.moox.collector.CollectMgr": {HostIds: []string{"control"}}}}, err
}

func (w *collectorWire) CreateTask(_ context.Context, req *pb.CreateTaskReq) (*pb.CreateTaskRsp, error) {
	if req.GetTask().GetSpaceId() != "stockcn" || req.GetTask().GetEnabled() || req.GetTask().GetCollectParams().GetFields()["frequency"].GetStringValue() != "1m" {
		return nil, fmt.Errorf("invalid disabled task or scope")
	}
	w.calls <- "CreateTask"
	return &pb.CreateTaskRsp{TaskId: "task-1"}, nil
}

func (w *collectorWire) GetTaskDetail(_ context.Context, req *pb.GetTaskDetailReq) (*pb.GetTaskDetailRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetTaskId() != "task-1" {
		return nil, fmt.Errorf("invalid detail scope")
	}
	w.calls <- "GetTaskDetail"
	params, _ := structpb.NewStruct(map[string]any{"frequency": "1m", "target_dataset_id": "owned-result"})
	return &pb.GetTaskDetailRsp{Task: &pb.CollectionTask{SpaceId: "stockcn", TaskId: "task-1", TaskName: "task", CollectParams: params}}, nil
}

func (w *collectorWire) UpdateTask(_ context.Context, req *pb.UpdateTaskReq) (*pb.UpdateTaskRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetTaskId() != "task-1" || !req.GetTask().GetEnabled() || req.GetTask().GetCollectParams().GetFields()["target_dataset_id"].GetStringValue() != "owned-result" {
		return nil, fmt.Errorf("task ownership changed during enable")
	}
	w.calls <- "UpdateTask"
	return &pb.UpdateTaskRsp{}, nil
}

func (w *collectorWire) GetTaskList(_ context.Context, req *pb.GetTaskListReq) (*pb.GetTaskListRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetPage().GetSize() != 1000 {
		return nil, fmt.Errorf("invalid list scope")
	}
	w.calls <- "GetTaskList"
	return &pb.GetTaskListRsp{Tasks: []*pb.CollectionTask{{SpaceId: "stockcn", TaskId: "task-1"}}}, nil
}

func (w *collectorWire) DisableTask(_ context.Context, req *pb.DisableTaskReq) (*pb.DisableTaskRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetTaskId() != "task-1" {
		return nil, fmt.Errorf("invalid disable scope")
	}
	w.calls <- "DisableTask"
	return &pb.DisableTaskRsp{}, nil
}

func (w *collectorWire) DeleteTask(_ context.Context, req *pb.DeleteTaskReq) (*pb.DeleteTaskRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetTaskId() != "task-1" || !req.GetDeleteResultData() {
		return nil, fmt.Errorf("invalid delete scope")
	}
	w.calls <- "DeleteTask"
	return &pb.DeleteTaskRsp{}, nil
}

func (w *collectorWire) GetTaskResultInventory(_ context.Context, req *pb.GetTaskResultInventoryReq) (*pb.GetTaskResultInventoryRsp, error) {
	if req.GetSpaceId() != "stockcn" || req.GetSnapshotId() != "snapshot-1" {
		return nil, fmt.Errorf("inventory scope or generation changed")
	}
	w.calls <- "GetTaskResultInventory"
	return &pb.GetTaskResultInventoryRsp{SnapshotId: "snapshot-1"}, nil
}

func TestCollectorTaskWorkflowUsesNativeGatewayJSONAndInventoryPB(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	wire := &collectorWire{calls: make(chan string, 8)}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterCollectMgrService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	root := t.TempDir()
	t.Setenv("HOME", root)
	operator := filepath.Join(root, ".config", "moox")
	require.NoError(t, os.MkdirAll(operator, 0o700))
	knownHosts := filepath.Join(operator, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, nil, 0o600))
	snapshot := &setupconfig.Snapshot{}
	snapshot.Manifest.ControlHost = testfixture.GatewaySSH(t, "control", listener.Addr().String(), knownHosts)
	require.NoError(t, os.WriteFile(filepath.Join(operator, "gateway-client.yaml"), []byte("caller: moox-cli\nkey_id: admin-assigned-cli-7\nkey_file: caller-moox-cli.key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(operator, "caller-moox-cli.key"), []byte("operator-signing-secret-at-least-32-bytes"), 0o600))
	gateway, err := gatewayio.Open(context.Background(), snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	c := &Client{Gateway: gateway}
	ctx := context.Background()
	id, err := c.CreateTask(ctx, "stockcn", "task", "kline", "moox-cli", []string{"cn_a_share"}, map[string]any{"frequency": "1m"}, nil)
	require.NoError(t, err)
	require.Equal(t, "task-1", id)
	require.NoError(t, c.EnableCollectionTask(ctx, "stockcn", id))
	tasks, err := c.ListEnabledTasks(ctx, "stockcn")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, id, tasks[0].TaskID)
	require.NoError(t, c.DisableTask(ctx, "stockcn", id))
	require.NoError(t, c.DeleteTask(ctx, "stockcn", id, true))
	var inventory pb.GetTaskResultInventoryRsp
	require.NoError(t, gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetTaskResultInventory", &pb.GetTaskResultInventoryReq{SpaceId: "stockcn", SnapshotId: "snapshot-1"}, &inventory))
	require.Equal(t, "snapshot-1", inventory.GetSnapshotId())
	for _, method := range []string{"CreateTask", "GetTaskDetail", "UpdateTask", "GetTaskList", "DisableTask", "DeleteTask", "GetTaskResultInventory"} {
		select {
		case got := <-wire.calls:
			require.Equal(t, method, got)
		case <-time.After(time.Second):
			t.Fatalf("missing native call %s", method)
		}
	}
	_, err = New("http://127.0.0.1:1").ListEnabledTasks(ctx, "stockcn")
	require.ErrorContains(t, err, "SSH gateway")
	err = c.CallJSON(ctx, "POST", "/api/admin/collectmgr/GetTaskList", map[string]any{}, &tasks)
	require.ErrorContains(t, err, "HTTP routes have been removed")
}
