package command

import (
	"bytes"
	"context"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

type exclusionFakeGateway struct {
	methods []string
	reqs    []any
}

func (g *exclusionFakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.methods = append(g.methods, service+"/"+method)
	g.reqs = append(g.reqs, req)
	ok := &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}
	switch out := rsp.(type) {
	case *collectorpb.SetSubjectExclusionsRsp:
		in := req.(*collectorpb.SetSubjectExclusionsReq)
		out.RetInfo, out.Exclusions = ok, []*collectorpb.SubjectExclusion{{SubjectId: in.GetSubjectIds()[0], MarketType: in.GetMarketType(), Reason: in.GetReason(), ExpiresAt: in.GetExpiresAt()}}
	case *collectorpb.ClearSubjectExclusionsRsp:
		out.RetInfo, out.Removed = ok, 2
	case *collectorpb.ListSubjectExclusionsRsp:
		out.RetInfo, out.Exclusions = ok, []*collectorpb.SubjectExclusion{{SubjectId: "BTC-USDT", Reason: "公告下架"}}
	}
	return nil
}
func (g *exclusionFakeGateway) Close() error { return nil }
func (g *exclusionFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func TestCollectorSubjectExclusionCommands(t *testing.T) {
	gateway := &exclusionFakeGateway{}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
	ctx := context.Background()
	var out bytes.Buffer

	require.ErrorContains(t, runSubjectExclude(ctx, &out, subjectExclusionFlags{SpaceID: "crypto", Subjects: []string{"BTC-USDT"}}), "--reason")
	require.ErrorContains(t, runSubjectExclude(ctx, &out, subjectExclusionFlags{SpaceID: "crypto", Reason: "x"}), "--subjects")
	require.ErrorContains(t, runSubjectExclusions(ctx, &out, subjectExclusionFlags{}), "--space-id")
	require.Empty(t, gateway.methods)

	before := time.Now()
	require.NoError(t, runSubjectExclude(ctx, &out, subjectExclusionFlags{SpaceID: "crypto", Subjects: []string{" BTC-USDT ", ""}, Market: "swap", Reason: "公告下架", ExpiresIn: 72 * time.Hour}))
	set := gateway.reqs[0].(*collectorpb.SetSubjectExclusionsReq)
	require.Equal(t, []string{"BTC-USDT"}, set.GetSubjectIds())
	require.Equal(t, "swap", set.GetMarketType())
	require.Contains(t, set.GetOperator(), "moox-cli:")
	expires, err := time.Parse(time.RFC3339, set.GetExpiresAt())
	require.NoError(t, err)
	require.WithinDuration(t, before.Add(72*time.Hour), expires, 2*time.Second)
	require.Contains(t, out.String(), `"market": "swap"`)

	out.Reset()
	require.NoError(t, runSubjectUnexclude(ctx, &out, subjectExclusionFlags{SpaceID: "crypto", Subjects: []string{"BTC-USDT", "ETH-USDT"}}))
	require.Contains(t, out.String(), `"removed": 2`)
	out.Reset()
	require.NoError(t, runSubjectExclusions(ctx, &out, subjectExclusionFlags{SpaceID: "crypto"}))
	require.Contains(t, out.String(), `"market": "all"`)
	require.Equal(t, []string{
		"trpc.moox.collector.CollectMgr/SetSubjectExclusions",
		"trpc.moox.collector.CollectMgr/ClearSubjectExclusions",
		"trpc.moox.collector.CollectMgr/ListSubjectExclusions",
	}, gateway.methods)
}
