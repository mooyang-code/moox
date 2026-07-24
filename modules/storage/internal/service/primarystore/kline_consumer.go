package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

// KlineConsumer writes streamcalc's closed-bar events through PrimaryStore.
// The DataNode outbox created by MergeTimeSeriesRows remains the only producer
// of DatasetRowsUpserted, so the event chain is:
// Tick -> Streamcalc -> KlineClosed -> PrimaryStore -> DataNode outbox.
type KlineConsumer struct {
	consumer   *events.Consumer
	service    *Service
	publisher  events.MessagePublisher
	datasetID  string
	auth       *pb.AuthInfo
	consumerID string
}

func NewKlineConsumer(consumer *events.Consumer, service *Service, publisher events.MessagePublisher, datasetID string, auth *pb.AuthInfo) (*KlineConsumer, error) {
	if consumer == nil || service == nil || publisher == nil {
		return nil, errors.New("kline consumer dependencies are required")
	}
	if strings.TrimSpace(datasetID) == "" {
		return nil, errors.New("kline dataset_id is required")
	}
	return &KlineConsumer{consumer: consumer, service: service, publisher: publisher, datasetID: datasetID, auth: auth, consumerID: "storage-primary-kline"}, nil
}

func (c *KlineConsumer) Run(ctx context.Context, batch int) error {
	if c == nil || c.consumer == nil || c.service == nil {
		return errors.New("kline consumer is not initialized")
	}
	if batch <= 0 {
		batch = 32
	}
	for ctx.Err() == nil {
		deliveries, fetchErr := c.consumer.Fetch(ctx, batch)
		for _, delivery := range deliveries {
			if err := c.handle(ctx, delivery); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// handle has already NAK-ed transient failures. Keep the
				// consumer alive so JetStream can redeliver after the delay.
				continue
			}
		}
		if fetchErr != nil && !errors.Is(fetchErr, nats.ErrTimeout) {
			return fetchErr
		}
	}
	return ctx.Err()
}

func (c *KlineConsumer) handle(ctx context.Context, delivery *events.EventDelivery) error {
	if delivery == nil || delivery.Delivery == nil {
		return errors.New("kline event delivery is nil")
	}
	if delivery.Err != nil {
		return c.reject(ctx, delivery.Delivery, delivery.Err)
	}
	payload, ok := delivery.Payload.(*marketpb.KlineClosed)
	if !ok {
		return c.reject(ctx, delivery.Delivery, fmt.Errorf("unexpected kline payload %T", delivery.Payload))
	}
	if delivery.Message.GetSubjectId() != payload.GetSymbol() || payload.GetWindowStart() == nil || payload.GetWindowEnd() == nil {
		return c.reject(ctx, delivery.Delivery, errors.New("kline event identity or window is invalid"))
	}
	row := &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
		SpaceId: delivery.Message.GetSpaceId(), DatasetId: c.datasetID, SubjectId: payload.GetSymbol(),
		Freq: payload.GetFrequency(), DataTime: payload.GetWindowStart().AsTime().UTC().Format(time.RFC3339Nano),
	}, Fields: []*pb.FieldValue{
		doubleField("open", payload.GetOpen()), doubleField("high", payload.GetHigh()), doubleField("low", payload.GetLow()),
		doubleField("close", payload.GetClose()), doubleField("volume", payload.GetVolume()), doubleField("quote_volume", payload.GetQuoteVolume()),
		intField("trade_num", payload.GetTradeCount()),
	}}
	rsp, err := c.service.MergeTimeSeriesRows(ctx, &pb.MergeTimeSeriesRowsReq{AuthInfo: c.auth, Rows: []*pb.TimeSeriesRow{row}})
	if err != nil {
		if nakErr := delivery.Delivery.Nak(ctx, time.Second); nakErr != nil {
			return errors.Join(err, nakErr)
		}
		return err
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		writeErr := errors.New("primary store rejected kline row")
		if rsp != nil && rsp.GetRetInfo() != nil && rsp.GetRetInfo().GetMsg() != "" {
			writeErr = errors.New(rsp.GetRetInfo().GetMsg())
		}
		if nakErr := delivery.Delivery.Nak(ctx, time.Second); nakErr != nil {
			return errors.Join(writeErr, nakErr)
		}
		return writeErr
	}
	return delivery.Delivery.Ack(ctx)
}

func (c *KlineConsumer) reject(ctx context.Context, delivery *jetstream.Delivery, reason error) error {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	if err := events.PublishRejected(ctx, c.publisher, registry, delivery, reason.Error(), c.consumerID); err != nil {
		if nakErr := delivery.Nak(ctx, time.Second); nakErr != nil {
			return errors.Join(err, nakErr)
		}
		return err
	}
	return delivery.Term(ctx)
}

func doubleField(name string, value float64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func intField(name string, value int64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}}}
}
