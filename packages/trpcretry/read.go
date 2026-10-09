// Package trpcretry provides bounded retry policies for idempotent tRPC calls.
package trpcretry

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-utils/copyutils"
)

// ReadOnly returns a bounded retry filter for explicitly reviewed read calls.
// It makes at most two attempts, separated by 100ms or server pushback, within
// the original context deadline. Each attempt has isolated wire metadata and output.
func ReadOnly() filter.ClientFilter {
	return readOnly
}

func readOnly(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
	var responseType reflect.Type
	if rsp != nil {
		value := reflect.ValueOf(rsp)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			return fmt.Errorf("read-only retry response must be a non-nil pointer")
		}
		responseType = value.Type().Elem()
	}
	ctx = client.WithOptionsImmutable(ctx)
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return timeoutError()
		}
		err, delay, final := invokeAttempt(ctx, req, rsp, responseType, next, attempt == 1)
		if final {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return timeoutError()
		case <-timer.C:
		}
	}
	panic("unreachable read-only retry attempt")
}

func invokeAttempt(ctx context.Context, req, rsp interface{}, responseType reflect.Type, next filter.ClientHandleFunc, last bool) (error, time.Duration, bool) {
	original := codec.Message(ctx)
	attemptCtx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	codec.CopyMsg(message, original)
	// codec.CopyMsg clones maps but shares their byte slices.
	message.WithClientMetaData(cloneMetadata(original.ClientMetaData()))
	message.WithServerMetaData(cloneMetadata(original.ServerMetaData()))
	requestHead, err := cloneHeader(original.ClientReqHead())
	if err != nil {
		return err, 0, true
	}
	responseHead, err := cloneHeader(original.ClientRspHead())
	if err != nil {
		return err, 0, true
	}
	message.WithClientReqHead(requestHead)
	message.WithClientRspHead(responseHead)
	var output interface{}
	if responseType != nil {
		output = reflect.New(responseType).Interface()
	}
	err = next(attemptCtx, req, output)
	delay := 100 * time.Millisecond
	if value, ok := message.ClientMetaData()["trpc-pushback-delay"]; ok {
		if parsed, parseErr := time.ParseDuration(string(value)); parseErr == nil {
			delay = parsed
		}
	}
	code := errs.Code(err)
	final := last || err == nil || (code != errs.RetClientNetErr && code != errs.RetClientTimeout) || delay < 0
	if !final {
		return err, delay, false
	}
	// Preserve the header objects held by the tRPC stub while returning the final
	// attempt's metadata. Failed attempts never modify the caller's response.
	originalRequestHead, originalResponseHead := original.ClientReqHead(), original.ClientRspHead()
	codec.CopyMsg(original, message)
	if originalRequestHead != nil && requestHead != nil {
		if copyErr := copyOutput(originalRequestHead, message.ClientReqHead()); copyErr != nil {
			return copyErr, 0, true
		}
		original.WithClientReqHead(originalRequestHead)
	}
	if originalResponseHead != nil && responseHead != nil {
		if copyErr := copyOutput(originalResponseHead, message.ClientRspHead()); copyErr != nil {
			return copyErr, 0, true
		}
		original.WithClientRspHead(originalResponseHead)
	}
	if err == nil {
		err = copyOutput(rsp, output)
	}
	return err, 0, true
}

func cloneHeader(header interface{}) (interface{}, error) {
	if message, ok := header.(proto.Message); ok {
		return proto.Clone(message), nil
	}
	return copyutils.DeepCopy(header)
}

func cloneMetadata(metadata codec.MetaData) codec.MetaData {
	if metadata == nil {
		return nil
	}
	cloned := make(codec.MetaData, len(metadata))
	for key, value := range metadata {
		cloned[key] = bytes.Clone(value)
	}
	return cloned
}

func copyOutput(dst, src interface{}) error {
	if message, ok := dst.(proto.Message); ok {
		other, ok := src.(proto.Message)
		if !ok || reflect.TypeOf(message) != reflect.TypeOf(other) {
			return fmt.Errorf("read-only retry output type mismatch")
		}
		proto.Reset(message)
		proto.Merge(message, other)
		return nil
	}
	return copyutils.ShallowCopy(dst, src)
}

func timeoutError() error {
	return errs.NewFrameError(errs.RetClientTimeout, "request timeout")
}
