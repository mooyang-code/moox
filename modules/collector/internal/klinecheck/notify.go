package klinecheck

import (
	"context"
	"fmt"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// FactorNotifier 把补齐通知发给因子管理服务（FactorMgr.NotifyPeriodsFilled），引擎经心跳收到后立即推进连续水位。
func FactorNotifier(gateway gatewayclient.Invoker) func(context.Context, []FilledRange) error {
	return func(ctx context.Context, filled []FilledRange) error {
		if gateway == nil || len(filled) == 0 {
			return nil
		}
		request := &factorpb.NotifyPeriodsFilledReq{}
		for _, item := range filled {
			message := &factorpb.PeriodsFilled{SpaceId: item.SpaceID, DatasetId: item.DatasetID, Frequency: item.Frequency,
				From: item.From.Unix(), To: item.To.Unix(), AbsentSubjects: item.Absent}
			for _, series := range item.AbsentSeries {
				message.AbsentSeries = append(message.AbsentSeries, &factorpb.SeriesRef{SubjectId: series.SubjectID, SeriesTag: series.SeriesTag})
			}
			request.Filled = append(request.Filled, message)
		}
		metadata := gatewayclient.CallMetadataFromContext(ctx)
		metadata.SpaceID = filled[0].SpaceID
		var response factorpb.NotifyPeriodsFilledRsp
		if err := gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), "trpc.moox.factor.FactorMgr", "NotifyPeriodsFilled", request, &response); err != nil {
			return err
		}
		if code := response.GetRetInfo().GetCode(); code != 0 {
			return fmt.Errorf("notify periods filled: %s", response.GetRetInfo().GetMsg())
		}
		return nil
	}
}
