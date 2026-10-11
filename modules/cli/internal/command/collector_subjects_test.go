package command

import (
	"bytes"
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type subjectsFakeGateway struct {
	service, method string
	req             *collectorpb.SyncSubjectsReq
	rsp             *collectorpb.SyncSubjectsRsp
	closed          bool
}

func (g *subjectsFakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.service, g.method = service, method
	g.req = req.(*collectorpb.SyncSubjectsReq)
	if out, ok := rsp.(*collectorpb.SyncSubjectsRsp); ok && g.rsp != nil {
		out.RetInfo, out.Tags, out.Attributes = g.rsp.RetInfo, g.rsp.Tags, g.rsp.Attributes
	}
	return nil
}
func (g *subjectsFakeGateway) Close() error { g.closed = true; return nil }
func (g *subjectsFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func useSubjectsGateway(t *testing.T, gateway *subjectsFakeGateway) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
}

func TestCollectorSubjectsSyncPrintsEveryTagAndPassesFilters(t *testing.T) {
	gateway := &subjectsFakeGateway{rsp: &collectorpb.SyncSubjectsRsp{
		RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS},
		Tags:    []*collectorpb.SubjectTagSyncResult{{SpaceId: "crypto", TagId: "binance_spot", Status: "success", Subjects: 678}},
	}}
	useSubjectsGateway(t, gateway)
	var out bytes.Buffer
	err := runCollectorSubjectsSync(context.Background(), &out, collectorSubjectsSyncFlags{SpaceID: " crypto ", TagID: "binance_spot", Attributes: true})
	require.NoError(t, err)
	assert.Equal(t, "trpc.moox.collector.CollectMgr", gateway.service)
	assert.Equal(t, "SyncSubjects", gateway.method)
	assert.Equal(t, "crypto", gateway.req.GetSpaceId())
	assert.Equal(t, "binance_spot", gateway.req.GetTagId())
	assert.True(t, gateway.req.GetIncludeAttributes())
	assert.True(t, gateway.closed)
	assert.Contains(t, out.String(), `"subjects": 678`)
	assert.Contains(t, out.String(), `"status": "success"`)
}

func TestCollectorSubjectsSyncFailsWhenAnyTagFailsButStillPrintsResults(t *testing.T) {
	gateway := &subjectsFakeGateway{rsp: &collectorpb.SyncSubjectsRsp{
		RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS},
		Tags: []*collectorpb.SubjectTagSyncResult{
			{SpaceId: "crypto", TagId: "binance_spot", Status: "success", Subjects: 678},
			{SpaceId: "crypto", TagId: "binance_swap", Status: "failed", Message: "HTTP status 302"},
		},
	}}
	useSubjectsGateway(t, gateway)
	var out bytes.Buffer
	err := runCollectorSubjectsSync(context.Background(), &out, collectorSubjectsSyncFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 tag or attribute sync(s) failed")
	assert.Contains(t, out.String(), "HTTP status 302", "the failure reason must reach the operator")
	assert.Contains(t, out.String(), `"status": "failed"`)
}

func TestCollectorSubjectsSyncSurfacesRPCErrorsAndBadFlags(t *testing.T) {
	gateway := &subjectsFakeGateway{rsp: &collectorpb.SyncSubjectsRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_CONFLICT, Msg: "subject sync is already running"}}}
	useSubjectsGateway(t, gateway)
	var out bytes.Buffer
	err := runCollectorSubjectsSync(context.Background(), &out, collectorSubjectsSyncFlags{})
	require.ErrorContains(t, err, "subject sync is already running")
	assert.Empty(t, out.String())

	gateway.method = ""
	err = runCollectorSubjectsSync(context.Background(), &out, collectorSubjectsSyncFlags{TagID: "binance_spot"})
	require.ErrorContains(t, err, "--tag-id requires --space-id")
	assert.Empty(t, gateway.method, "invalid flags must not reach the gateway")
}
