package trpcretry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

func TestReadOnlyBoundsNetworkRetries(t *testing.T) {
	attempts := 0
	err := ReadOnly()(context.Background(), nil, nil, func(context.Context, interface{}, interface{}) error {
		attempts++
		return errs.New(errs.RetClientNetErr, "network unavailable")
	})
	require.Error(t, err)
	require.Equal(t, 2, attempts)
}

func TestReadOnlyDoesNotRetryBusinessErrors(t *testing.T) {
	attempts := 0
	err := ReadOnly()(context.Background(), nil, nil, filter.ClientHandleFunc(func(context.Context, interface{}, interface{}) error {
		attempts++
		return errs.New(100101, "invalid request")
	}))
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestReadOnlyIsolatesAttemptsAndReturnsFinalMetadata(t *testing.T) {
	ctx, message := codec.WithNewMessage(context.Background())
	defer codec.PutBackMessage(message)
	message.WithClientRPCName("/trpc.test.Service/Get")
	message.WithClientMetaData(codec.MetaData{"original": []byte("value")})
	message.WithServerMetaData(codec.MetaData{"incoming": []byte("input")})
	requestHead := wrapperspb.String("request")
	responseHead := wrapperspb.String("initial")
	message.WithClientReqHead(requestHead)
	message.WithClientRspHead(responseHead)
	rsp := wrapperspb.String("unchanged")
	attempts := 0
	err := ReadOnly()(ctx, nil, rsp, func(attemptCtx context.Context, _, output interface{}) error {
		attempts++
		require.True(t, client.IsOptionsImmutable(attemptCtx))
		attemptMessage := codec.Message(attemptCtx)
		require.Equal(t, "/trpc.test.Service/Get", attemptMessage.ClientRPCName())
		require.Equal(t, "value", string(attemptMessage.ClientMetaData()["original"]))
		require.Equal(t, "input", string(attemptMessage.ServerMetaData()["incoming"]))
		require.Equal(t, "request", attemptMessage.ClientReqHead().(*wrapperspb.StringValue).Value)
		require.Equal(t, "initial", attemptMessage.ClientRspHead().(*wrapperspb.StringValue).Value)
		require.Empty(t, output.(*wrapperspb.StringValue).Value)
		require.Equal(t, "unchanged", rsp.Value)
		attemptMessage.ClientMetaData()["original"][0] = 'X'
		attemptMessage.ServerMetaData()["incoming"][0] = 'X'
		attemptMessage.ClientMetaData()["trpc-pushback-delay"] = []byte("0s")
		attemptMessage.ClientReqHead().(*wrapperspb.StringValue).Value = "sent"
		attemptMessage.ClientRspHead().(*wrapperspb.StringValue).Value = "received"
		output.(*wrapperspb.StringValue).Value = "final"
		if attempts == 1 {
			return errs.NewFrameError(errs.RetClientNetErr, "try again")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.Equal(t, "final", rsp.Value)
	require.Same(t, requestHead, message.ClientReqHead())
	require.Same(t, responseHead, message.ClientRspHead())
	require.Equal(t, "sent", requestHead.Value)
	require.Equal(t, "received", responseHead.Value)
	require.Equal(t, "Xalue", string(message.ClientMetaData()["original"]))
}

func TestReadOnlyRawBodyAndFailedResponse(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "failure"}[success], func(t *testing.T) {
			ctx, message := codec.WithNewMessage(context.Background())
			defer codec.PutBackMessage(message)
			rsp := &codec.Body{Data: []byte("original")}
			attempts := 0
			err := ReadOnly()(ctx, nil, rsp, func(ctx context.Context, _, output interface{}) error {
				attempts++
				require.Empty(t, output.(*codec.Body).Data)
				output.(*codec.Body).Data = []byte("result")
				codec.Message(ctx).WithClientMetaData(codec.MetaData{"trpc-pushback-delay": []byte("0s")})
				if attempts == 2 && success {
					return nil
				}
				return errs.NewFrameError(errs.RetClientTimeout, "timeout")
			})
			require.Equal(t, 2, attempts)
			if success {
				require.NoError(t, err)
				require.Equal(t, "result", string(rsp.Data))
			} else {
				require.Equal(t, errs.RetClientTimeout, errs.Code(err))
				require.Equal(t, "original", string(rsp.Data))
			}
		})
	}
}

func TestReadOnlyHonorsCancellationAndPushback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pushback string
		canceled bool
		attempts int
	}{
		{name: "canceled", canceled: true, attempts: 0},
		{name: "cancel during backoff", pushback: "1h", attempts: 1},
		{name: "negative pushback", pushback: "-1s", attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if tc.canceled {
				cancel()
			}
			attempts := 0
			err := ReadOnly()(ctx, nil, nil, func(ctx context.Context, _, _ interface{}) error {
				attempts++
				codec.Message(ctx).WithClientMetaData(codec.MetaData{"trpc-pushback-delay": []byte(tc.pushback)})
				return errs.NewFrameError(errs.RetClientTimeout, "timeout")
			})
			require.Equal(t, errs.RetClientTimeout, errs.Code(err))
			require.Equal(t, tc.attempts, attempts)
		})
	}
}

func TestReadOnlyRejectsInvalidResponseWithoutCallingUpstream(t *testing.T) {
	for _, rsp := range []interface{}{42, (*codec.Body)(nil)} {
		err := ReadOnly()(context.Background(), nil, rsp, func(context.Context, interface{}, interface{}) error {
			t.Fatal("invalid response reached upstream")
			return nil
		})
		require.ErrorContains(t, err, "non-nil pointer")
	}
}
