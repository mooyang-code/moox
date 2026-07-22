package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type storageActivationClientStub struct {
	datasets      []*pb.Dataset
	checks        map[string]*pb.CheckDatasetActivationRsp
	listErr       error
	checkErr      error
	listCalls     int
	checkCalls    []string
	activateCalls int
	listDeadline  time.Time
	checkDeadline time.Time
}

type storageDoctorContextClient struct {
	rsp *monitorpb.GetDoctorContextRsp
}

func (s storageDoctorContextClient) GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	return s.rsp, nil
}

func (s *storageActivationClientStub) ListDatasets(ctx context.Context, _ *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	s.listCalls++
	s.listDeadline, _ = ctx.Deadline()
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &pb.ListDatasetsRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Datasets:   s.datasets,
		PageResult: &pb.PageResult{HasMore: false},
	}, nil
}

func (s *storageActivationClientStub) CheckDatasetActivation(ctx context.Context, req *pb.CheckDatasetActivationReq) (*pb.CheckDatasetActivationRsp, error) {
	s.checkDeadline, _ = ctx.Deadline()
	s.checkCalls = append(s.checkCalls, req.GetSpaceId()+"/"+req.GetDatasetId())
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	return s.checks[req.GetSpaceId()+"/"+req.GetDatasetId()], nil
}

func runStorageActivationReport(t *testing.T, client StorageActivationClient) core.Report {
	t.Helper()
	report, err := RunBootstrap(context.Background(), BootstrapOptions{
		NodeID:            "node-a",
		LocalNodeID:       "node-a",
		CheckIDs:          []string{storageDatasetActivationCheckID},
		StorageActivation: client,
		Now:               func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	return report
}

func activationResponse(ready bool, revision uint64, summary string) *pb.CheckDatasetActivationRsp {
	return &pb.CheckDatasetActivationRsp{
		RetInfo:         &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		DatasetRevision: revision,
		Ready:           ready,
		Checks:          []*pb.DatasetActivationCheck{{CheckId: "data_node_identity", Ready: ready, Summary: summary}},
	}
}

func TestStorageDatasetActivationIsReadOnlyAndStable(t *testing.T) {
	client := &storageActivationClientStub{
		datasets: []*pb.Dataset{
			{SpaceId: "stock", DatasetId: "z_dataset", Status: "disabled"},
			{SpaceId: "crypto", DatasetId: "z_dataset", Status: "disabled"},
			{SpaceId: "crypto", DatasetId: "a_dataset", Status: "disabled"},
			{SpaceId: "ignored", DatasetId: "active_dataset", Status: "active"},
		},
		checks: map[string]*pb.CheckDatasetActivationRsp{
			"crypto/a_dataset": activationResponse(true, 3, "secret-target=ip://10.0.0.1:20107"),
			"crypto/z_dataset": activationResponse(true, 7, "secret-target=ip://10.0.0.2:20107"),
			"stock/z_dataset":  activationResponse(true, 2, "secret-target=ip://10.0.0.3:20107"),
		},
	}

	report := runStorageActivationReport(t, client)
	check := report.CheckByID(storageDatasetActivationCheckID)
	require.NotNil(t, check)
	require.Equal(t, core.StatusPass, check.Status)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Equal(t, []string{"crypto/a_dataset", "crypto/z_dataset", "stock/z_dataset"}, client.checkCalls)
	require.Equal(t, 0, client.activateCalls)
	require.Len(t, check.Observations, 3)
	for _, observation := range check.Observations {
		require.NotContains(t, observation.Summary, "secret-target")
		require.NotContains(t, observation.Digest, "10.0.0")
		require.NotEmpty(t, observation.Digest)
	}
	require.Contains(t, check.Observations[0].Summary, "crypto/a_dataset")
}

func TestStorageDatasetActivationDegradesForFailedChecks(t *testing.T) {
	client := &storageActivationClientStub{
		datasets: []*pb.Dataset{{SpaceId: "crypto", DatasetId: "kline", Status: "disabled"}},
		checks: map[string]*pb.CheckDatasetActivationRsp{
			"crypto/kline": activationResponse(false, 4, "secret readiness detail"),
		},
	}
	report := runStorageActivationReport(t, client)
	check := report.CheckByID(storageDatasetActivationCheckID)
	require.Equal(t, core.StatusWarn, check.Status)
	require.Equal(t, core.ConclusionDegraded, report.Conclusion)
	require.Contains(t, check.Summary, "failed activation readiness checks")
	require.NotContains(t, check.Observations[0].Summary, "secret readiness detail")
}

func TestStorageDatasetActivationIsUnknownWhenMetadataIsUnreachable(t *testing.T) {
	client := &storageActivationClientStub{listErr: errors.New("dial tcp 10.0.0.1:20200: connection refused")}
	report := runStorageActivationReport(t, client)
	check := report.CheckByID(storageDatasetActivationCheckID)
	require.Equal(t, core.StatusUnknown, check.Status)
	require.Equal(t, core.ConclusionInconclusive, report.Conclusion)
	require.NotContains(t, check.Error, "10.0.0.1")
	require.Equal(t, 0, client.activateCalls)
}

func TestStorageDatasetActivationBoundsObservationsWithOmittedSummary(t *testing.T) {
	client := &storageActivationClientStub{checks: map[string]*pb.CheckDatasetActivationRsp{}}
	for i := 0; i < 20; i++ {
		datasetID := fmt.Sprintf("dataset_%02d", i)
		client.datasets = append(client.datasets, &pb.Dataset{SpaceId: "crypto", DatasetId: datasetID, Status: "disabled"})
		client.checks["crypto/"+datasetID] = activationResponse(true, uint64(i+1), "safe")
	}
	report := runStorageActivationReport(t, client)
	check := report.CheckByID(storageDatasetActivationCheckID)
	require.Len(t, check.Observations, core.MaxObservationsPerCheck)
	require.Contains(t, check.Observations[len(check.Observations)-1].Summary, "5 disabled Dataset observations omitted")
	require.True(t, strings.HasSuffix(check.Observations[0].Summary, "revision 1"))
}

func TestStorageDatasetActivationUsesBoundedTimeout(t *testing.T) {
	client := &storageActivationClientStub{listErr: context.DeadlineExceeded}
	report := runStorageActivationReport(t, client)
	check := report.CheckByID(storageDatasetActivationCheckID)
	require.Equal(t, core.StatusUnknown, check.Status)
	require.False(t, client.listDeadline.IsZero())
	remaining := time.Until(client.listDeadline)
	require.Greater(t, remaining, 0*time.Second)
	require.LessOrEqual(t, remaining, storageActivationCheckTimeout)
}

func TestDoctorBootstrapAndDiagnoseKeepDatasetStatusAndRevisionByteIdentical(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", Status: "disabled", Revision: 9}
	client := &storageActivationClientStub{
		datasets: []*pb.Dataset{dataset},
		checks: map[string]*pb.CheckDatasetActivationRsp{
			"crypto/kline": activationResponse(true, dataset.GetRevision(), "safe"),
		},
	}
	before, err := proto.Marshal(&pb.Dataset{Status: dataset.GetStatus(), Revision: dataset.GetRevision()})
	require.NoError(t, err)

	bootstrap := runStorageActivationReport(t, client)
	require.Equal(t, core.StatusPass, bootstrap.CheckByID(storageDatasetActivationCheckID).Status)

	_, err = RunDiagnose(context.Background(), DiagnoseOptions{
		CheckIDs: []string{"diagnose.context"},
		Client:   storageDoctorContextClient{rsp: &monitorpb.GetDoctorContextRsp{ManifestChecksum: "sha256:test"}},
	})
	require.NoError(t, err)
	after, err := proto.Marshal(&pb.Dataset{Status: dataset.GetStatus(), Revision: dataset.GetRevision()})
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, 0, client.activateCalls)
}
