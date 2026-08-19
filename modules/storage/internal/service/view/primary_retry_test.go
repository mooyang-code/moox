package view

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/errs"
)

type primaryHistoryRetryReader struct {
	remainingFailures int
	err               error
	calls             int
}

func (r *primaryHistoryRetryReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.calls++
	if r.remainingFailures > 0 {
		r.remainingFailures--
		return nil, r.err
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo()}, nil
}

func TestReadPrimaryTimeSeriesRowsRetriesTransportFailures(t *testing.T) {
	reader := &primaryHistoryRetryReader{
		remainingFailures: 2,
		err:               errs.New(errs.RetClientConnectFail, "tcp client transport connection pool"),
	}
	response, err := readPrimaryTimeSeriesRows(context.Background(), reader, &pb.ReadTimeSeriesRowsReq{})
	if err != nil || response == nil {
		t.Fatalf("read with retries = response=%v err=%v", response, err)
	}
	if reader.calls != primaryHistoryReadAttempts {
		t.Fatalf("calls=%d, want %d", reader.calls, primaryHistoryReadAttempts)
	}
}

func TestReadPrimaryTimeSeriesRowsStopsAfterThreeTransportFailures(t *testing.T) {
	reader := &primaryHistoryRetryReader{
		remainingFailures: primaryHistoryReadAttempts,
		err:               errors.New("tcp client transport connection pool"),
	}
	_, err := readPrimaryTimeSeriesRows(context.Background(), reader, &pb.ReadTimeSeriesRowsReq{})
	if err == nil {
		t.Fatal("expected exhausted Primary history retries")
	}
	if reader.calls != primaryHistoryReadAttempts {
		t.Fatalf("calls=%d, want %d", reader.calls, primaryHistoryReadAttempts)
	}
}

func TestReadPrimaryTimeSeriesRowsDoesNotRetryBusinessFailures(t *testing.T) {
	reader := &primaryHistoryRetryReader{
		remainingFailures: 1,
		err:               errs.New(100101, "invalid history request"),
	}
	_, err := readPrimaryTimeSeriesRows(context.Background(), reader, &pb.ReadTimeSeriesRowsReq{})
	if err == nil {
		t.Fatal("expected business failure")
	}
	if reader.calls != 1 {
		t.Fatalf("calls=%d, want 1", reader.calls)
	}
}

func TestReadPrimaryTimeSeriesRowsDoesNotRetryKnownBusinessMessageThatLooksTransient(t *testing.T) {
	reader := &primaryHistoryRetryReader{
		remainingFailures: 1,
		err:               errs.New(100101, "validation deadline exceeded"),
	}
	_, err := readPrimaryTimeSeriesRows(context.Background(), reader, &pb.ReadTimeSeriesRowsReq{})
	if err == nil {
		t.Fatal("expected business failure")
	}
	if reader.calls != 1 {
		t.Fatalf("calls=%d, want 1", reader.calls)
	}
}
