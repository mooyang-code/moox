package command

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type storageGateway struct{ gateway gatewayclient.Invoker }

func (s storageGateway) invoke(ctx context.Context, service, method string, request, response any, options []client.Option) error {
	if s.gateway == nil || len(options) != 0 {
		return errors.New("Storage gateway requires a client and forbids SDK option overrides")
	}
	return s.gateway.Invoke(ctx, service, method, request, response)
}

func (s storageGateway) ReadTimeSeriesRows(ctx context.Context, request *pb.ReadTimeSeriesRowsReq, options ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	response := &pb.ReadTimeSeriesRowsRsp{}
	err := s.invoke(ctx, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", request, response, options)
	return response, err
}

func (s storageGateway) EnsureDatasetPeriod(ctx context.Context, request *pb.PrimaryEnsureDatasetPeriodReq, options ...client.Option) (*pb.PrimaryEnsureDatasetPeriodRsp, error) {
	response := &pb.PrimaryEnsureDatasetPeriodRsp{}
	err := s.invoke(ctx, "trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod", request, response, options)
	return response, err
}

func (s storageGateway) GetDatasetPeriodStatus(ctx context.Context, request *pb.PrimaryGetDatasetPeriodStatusReq, options ...client.Option) (*pb.PrimaryGetDatasetPeriodStatusRsp, error) {
	response := &pb.PrimaryGetDatasetPeriodStatusRsp{}
	err := s.invoke(ctx, "trpc.moox.storage.PrimaryStore", "GetDatasetPeriodStatus", request, response, options)
	return response, err
}

func (s storageGateway) QueryTimeSeriesRows(ctx context.Context, request *pb.QueryTimeSeriesRowsReq, options ...client.Option) (*pb.QueryTimeSeriesRowsRsp, error) {
	response := &pb.QueryTimeSeriesRowsRsp{}
	err := s.invoke(ctx, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", request, response, options)
	return response, err
}
