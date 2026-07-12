// Package rpc exposes a read-only EventBus status surface.
package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	eventbusgen "github.com/mooyang-code/moox/modules/eventbus/proto/eventbusgen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/nats-io/nats.go"
)

type Options struct {
	Ready       func() bool
	Connections func() uint32
}

// StateSource is the read-only subset of JetStream required by the status RPC.
// Keeping the RPC layer on this narrow interface allows unit tests and
// future adapters to provide broker state without opening a socket.
type StateSource interface {
	StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	StreamsInfo(opts ...nats.JSOpt) <-chan *nats.StreamInfo
	Consumers(stream string, opts ...nats.JSOpt) <-chan *nats.ConsumerInfo
	ConsumersInfo(stream string, opts ...nats.JSOpt) <-chan *nats.ConsumerInfo
	ConsumerInfo(stream, consumer string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error)
}

type Service struct {
	eventbusgen.UnimplementedEventBusMgr
	js          StateSource
	cfg         *config.Config
	ready       func() bool
	connections func() uint32
}

func New(js StateSource, cfg *config.Config, opts Options) *Service {
	return &Service{js: js, cfg: cfg, ready: opts.Ready, connections: opts.Connections}
}

func (s *Service) GetOverview(ctx context.Context, _ *eventbusgen.GetOverviewReq) (*eventbusgen.GetOverviewRsp, error) {
	if err := s.statusErr(); err != nil {
		return &eventbusgen.GetOverviewRsp{RetInfo: retErr(err)}, nil
	}
	streams, err := s.streams(ctx)
	if err != nil {
		return &eventbusgen.GetOverviewRsp{RetInfo: retErr(err)}, nil
	}
	var messages, bytes, pending uint64
	var consumers uint32
	for _, info := range streams {
		if info == nil {
			continue
		}
		messages += info.State.Msgs
		bytes += info.State.Bytes
		consumers += uint32(info.State.Consumers)
		for c := range s.js.Consumers(info.Config.Name, nats.Context(ctx)) {
			if c != nil {
				pending += c.NumPending + uint64(maxInt(c.NumAckPending, 0))
			}
		}
	}
	ready := true
	if s.ready != nil {
		ready = s.ready()
	}
	connections := uint32(0)
	if s.connections != nil {
		connections = s.connections()
	}
	return &eventbusgen.GetOverviewRsp{RetInfo: retOK(), Overview: &eventbusgen.Overview{JetstreamReady: ready, Connections: connections, Streams: uint32(len(streams)), Consumers: consumers, Messages: messages, Bytes: bytes, TotalPending: pending}}, nil
}

func (s *Service) ListTopics(ctx context.Context, req *eventbusgen.ListTopicsReq) (*eventbusgen.ListTopicsRsp, error) {
	if err := s.statusErr(); err != nil {
		return &eventbusgen.ListTopicsRsp{RetInfo: retErr(err)}, nil
	}
	items := make([]config.TopicConfig, 0)
	for _, t := range s.cfg.Topics {
		items = append(items, t)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Topic < items[j].Topic })
	page, rows := paginateRows(req.GetPage(), items)
	out := make([]*eventbusgen.TopicInfo, 0, len(rows))
	for _, t := range rows {
		out = append(out, &eventbusgen.TopicInfo{Topic: t.Topic, Stream: t.Stream, Kind: t.Kind, PayloadContentType: t.PayloadContentType, PayloadVersion: t.PayloadVersion, Enabled: t.Enabled})
	}
	return &eventbusgen.ListTopicsRsp{RetInfo: retOK(), Topics: out, PageResult: page}, nil
}

func (s *Service) ListStreams(ctx context.Context, req *eventbusgen.ListStreamsReq) (*eventbusgen.ListStreamsRsp, error) {
	if err := s.statusErr(); err != nil {
		return &eventbusgen.ListStreamsRsp{RetInfo: retErr(err)}, nil
	}
	infos, err := s.streams(ctx)
	if err != nil {
		return &eventbusgen.ListStreamsRsp{RetInfo: retErr(err)}, nil
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Config.Name < infos[j].Config.Name })
	page, rows := paginateRows(req.GetPage(), infos)
	out := make([]*eventbusgen.StreamInfo, 0, len(rows))
	for _, info := range rows {
		out = append(out, streamInfo(info))
	}
	return &eventbusgen.ListStreamsRsp{RetInfo: retOK(), Streams: out, PageResult: page}, nil
}

func (s *Service) ListConsumers(ctx context.Context, req *eventbusgen.ListConsumersReq) (*eventbusgen.ListConsumersRsp, error) {
	if err := s.statusErr(); err != nil {
		return &eventbusgen.ListConsumersRsp{RetInfo: retErr(err)}, nil
	}
	streams := []string{}
	if name := strings.TrimSpace(req.GetStream()); name != "" {
		streams = append(streams, name)
	} else {
		infos, err := s.streams(ctx)
		if err != nil {
			return &eventbusgen.ListConsumersRsp{RetInfo: retErr(err)}, nil
		}
		for _, info := range infos {
			streams = append(streams, info.Config.Name)
		}
	}
	type row struct {
		stream string
		info   *nats.ConsumerInfo
	}
	rows := []row{}
	for _, stream := range streams {
		if _, err := s.js.StreamInfo(stream, nats.Context(ctx)); err != nil {
			return &eventbusgen.ListConsumersRsp{RetInfo: retErr(fmt.Errorf("stream not found"))}, nil
		}
		for info := range s.js.ConsumersInfo(stream, nats.Context(ctx)) {
			if info != nil {
				rows = append(rows, row{stream: stream, info: info})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].stream == rows[j].stream {
			return rows[i].info.Name < rows[j].info.Name
		}
		return rows[i].stream < rows[j].stream
	})
	page, selected := paginateRows(req.GetPage(), rows)
	out := make([]*eventbusgen.ConsumerInfo, 0, len(selected))
	for _, row := range selected {
		out = append(out, consumerInfo(row.info))
	}
	return &eventbusgen.ListConsumersRsp{RetInfo: retOK(), Consumers: out, PageResult: page}, nil
}

func (s *Service) GetConsumer(ctx context.Context, req *eventbusgen.GetConsumerReq) (*eventbusgen.GetConsumerRsp, error) {
	if err := s.statusErr(); err != nil {
		return &eventbusgen.GetConsumerRsp{RetInfo: retErr(err)}, nil
	}
	stream, name := strings.TrimSpace(req.GetStream()), strings.TrimSpace(req.GetName())
	if stream == "" || name == "" {
		return &eventbusgen.GetConsumerRsp{RetInfo: retErr(fmt.Errorf("stream and name are required"))}, nil
	}
	info, err := s.js.ConsumerInfo(stream, name, nats.Context(ctx))
	if err != nil {
		return &eventbusgen.GetConsumerRsp{RetInfo: retErr(fmt.Errorf("consumer not found"))}, nil
	}
	return &eventbusgen.GetConsumerRsp{RetInfo: retOK(), Consumer: consumerInfo(info)}, nil
}

func (s *Service) statusErr() error {
	if s == nil || s.js == nil || s.cfg == nil {
		return fmt.Errorf("eventbus is not ready")
	}
	if s.ready != nil && !s.ready() {
		return fmt.Errorf("eventbus is not ready")
	}
	return nil
}
func (s *Service) streams(ctx context.Context) ([]*nats.StreamInfo, error) {
	out := []*nats.StreamInfo{}
	if s.cfg != nil {
		for _, spec := range s.cfg.Streams {
			info, err := s.js.StreamInfo(spec.Name, nats.Context(ctx))
			if err != nil {
				return nil, fmt.Errorf("stream %q not found", spec.Name)
			}
			if info == nil {
				return nil, fmt.Errorf("stream %q info is empty", spec.Name)
			}
			out = append(out, info)
		}
		return out, nil
	}
	for info := range s.js.StreamsInfo(nats.Context(ctx)) {
		if info != nil {
			out = append(out, info)
		}
	}
	return out, nil
}
func streamInfo(info *nats.StreamInfo) *eventbusgen.StreamInfo {
	out := &eventbusgen.StreamInfo{Name: info.Config.Name, Subjects: append([]string(nil), info.Config.Subjects...), Retention: info.Config.Retention.String(), Storage: info.Config.Storage.String(), Messages: info.State.Msgs, Bytes: info.State.Bytes}
	if !info.State.FirstTime.IsZero() {
		out.FirstTime = info.State.FirstTime.UTC().Format(time.RFC3339Nano)
	}
	if !info.State.LastTime.IsZero() {
		out.LastTime = info.State.LastTime.UTC().Format(time.RFC3339Nano)
	}
	return out
}
func consumerInfo(info *nats.ConsumerInfo) *eventbusgen.ConsumerInfo {
	out := &eventbusgen.ConsumerInfo{Stream: info.Stream, Name: info.Name, FilterSubject: info.Config.FilterSubject, Pending: info.NumPending, AckPending: uint64(maxInt(info.NumAckPending, 0)), Redelivered: uint64(maxInt(info.NumRedelivered, 0))}
	if info.Delivered.Last != nil {
		out.LastDeliveredAt = info.Delivered.Last.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func retOK() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "ok"} }
func retErr(err error) *commonpb.RetInfo {
	code := commonpb.ErrorCode_INNER_ERR
	message := strings.ToLower(sanitize(err))
	if strings.Contains(message, "not found") {
		code = commonpb.ErrorCode_NOT_FOUND
	}
	if strings.Contains(message, "required") || strings.Contains(message, "invalid") {
		code = commonpb.ErrorCode_INVALID_PARAM
	}
	return &commonpb.RetInfo{Code: code, Msg: sanitize(err)}
}
func sanitize(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	for _, token := range []string{"password", "secret", "credential", "store_dir", "/"} {
		if strings.Contains(msg, token) {
			return "eventbus operation failed"
		}
	}
	return err.Error()
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func paginateRows[T any](req *commonpb.Page, rows []T) (*commonpb.PageResult, []T) {
	page, size := pageParams(req)
	start := (page - 1) * size
	if start > len(rows) {
		start = len(rows)
	}
	end := start + size
	if end > len(rows) {
		end = len(rows)
	}
	return pageResult(page, size, len(rows), end < len(rows)), rows[start:end]
}
func pageParams(req *commonpb.Page) (int, int) {
	page, size := 1, 50
	if req != nil {
		if req.Page > 0 {
			page = int(req.Page)
		}
		if req.Size > 0 {
			size = int(req.Size)
		}
	}
	if size > 200 {
		size = 200
	}
	return page, size
}
func pageResult(page, size, total int, more bool) *commonpb.PageResult {
	return &commonpb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(total), HasMore: more, TotalState: commonpb.TotalState_EXACT}
}
