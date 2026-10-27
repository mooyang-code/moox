package sysdeploy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

// EventBus 维护：Storage 的维护命令（重置 View 消费者、清除数据集事件等）要删除消费者、清除主题里的消息，
// 这需要 EventBus 的内部管理员令牌。令牌只存在 Admin 的数据库里，由 Admin 代为执行，
// 调用方（storage-view 的网关身份）不需要在自己的主机上放管理员凭据文件。
//
// 代执行的范围是固定的：只有 Storage View 的流、Storage View 的消费者和数据集事件的确切主题，
// 拿到调用身份也不能借此清空别的流、删别的消费者或用通配主题清掉整条流。

const (
	// eventBusAdminRole 是 EventBus 内部管理员角色，对应数据库里的令牌 eventbus_internal_admin。
	eventBusAdminRole = "eventbus-internal-admin"
	// eventBusConnectTimeout 是连接 EventBus 的超时。
	eventBusConnectTimeout = 15 * time.Second
	// eventBusRequestTimeout 是单次 JetStream 管理请求的上限：繁忙的流上清除与删除消费者可能超过默认的 5 秒。
	eventBusRequestTimeout = 2 * time.Minute
	// maxEventBusTargets 限制一次请求涉及的消费者或主题数。
	maxEventBusTargets = 256
)

// eventBusSession 是维护操作用到的 JetStream 能力，测试里可以替换。
type eventBusSession interface {
	StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	ConsumerInfo(stream, durable string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error)
	DeleteConsumer(stream, durable string, opts ...nats.JSOpt) error
	PurgeStream(stream string, opts ...nats.JSOpt) error
	Close()
}

type natsEventBusSession struct {
	nats.JetStreamContext
	conn *nats.Conn
}

func (s natsEventBusSession) Close() { s.conn.Close() }

// eventBusTarget 是连接 EventBus 所需的地址和管理员身份。
type eventBusTarget struct {
	address string
	token   string
	caPEM   string
}

// resolveEventBusTarget 按组件部署与库里的管理员令牌得出连接目标。EventBus 与 Admin 在同一主机时走本机回环
// （管理员角色的证书覆盖 127.0.0.1），否则走对外地址。
func (s *ServiceImpl) resolveEventBusTarget(ctx context.Context) (eventBusTarget, error) {
	endpoint, err := ResolveEventBusEndpoint(ctx, s.db, s.adminNodeID)
	if err != nil {
		return eventBusTarget{}, fmt.Errorf("查找 EventBus 位置失败: %w", err)
	}
	target := eventBusTarget{address: endpoint.URL}
	if endpoint.HostID == s.adminNodeID {
		parsed, err := ValidateEventBusNATSURL(endpoint.URL)
		if err != nil {
			return eventBusTarget{}, err
		}
		target.address = "tls://" + net.JoinHostPort("127.0.0.1", parsed.Port())
	}
	secrets, _, err := secretdao.NewSecretDAO(s.db).List(ctx, 0, 100, &secretdao.SecretFilters{Category: "eventbus", Provider: "moox_eventbus", Status: "active"})
	if err != nil {
		return eventBusTarget{}, fmt.Errorf("读取 EventBus 凭据失败: %w", err)
	}
	for _, secret := range secrets {
		switch secret.KeyID {
		case "eventbus_internal_admin":
			target.token = secret.SecretValue
		case "eventbus_tls_ca":
			target.caPEM = secret.SecretValue
		}
	}
	if target.token == "" || target.caPEM == "" {
		return eventBusTarget{}, errors.New("EventBus 的管理员令牌或 TLS CA 尚未生成，请先执行 eventbus-credentials ensure")
	}
	return target, nil
}

// eventBusRequest 为单次 JetStream 请求派生独立的两分钟上限。nats.go 遇到带截止时间的 Context 会直接使用它而忽略
// JetStream 的 MaxWait，所以不能指望 MaxWait 限制单次请求：一个无响应的请求会耗尽整个 RPC 的截止时间，
// 连此前已完成的删除或清除结果都来不及返回。
func eventBusRequest(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, eventBusRequestTimeout)
}

// connectEventBus 用管理员令牌连上 EventBus 并返回 JetStream 会话；连不上时立即失败，不做后台重试。
func connectEventBus(target eventBusTarget) (eventBusSession, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(target.caPEM)) {
		return nil, errors.New("EventBus 的 TLS CA 不含有效证书")
	}
	conn, err := nats.Connect(target.address,
		nats.Name("moox-admin-eventbus-ops"),
		nats.UserInfo(eventBusAdminRole, target.token),
		nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}),
		nats.TLSHandshakeFirst(),
		nats.Timeout(eventBusConnectTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 EventBus %s 失败: %w", target.address, err)
	}
	js, err := conn.JetStream(nats.MaxWait(eventBusRequestTimeout))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("打开 EventBus JetStream 失败: %w", err)
	}
	return natsEventBusSession{JetStreamContext: js, conn: conn}, nil
}

func (s *ServiceImpl) openEventBusSession(ctx context.Context) (eventBusSession, error) {
	if s.openEventBus != nil {
		return s.openEventBus(ctx)
	}
	target, err := s.resolveEventBusTarget(ctx)
	if err != nil {
		return nil, err
	}
	return connectEventBus(target)
}

func eventBusRet(code pb.ErrorCode, format string, args ...any) *pb.RetInfo {
	return retErr(code, fmt.Sprintf(format, args...))
}

// checkEventBusStream 只放行 Storage View 使用的流。
func checkEventBusStream(stream string) error {
	if stream != events.StorageViewConsumerStream {
		return fmt.Errorf("不支持的流 %q，只允许 %s", stream, events.StorageViewConsumerStream)
	}
	return nil
}

// checkEventBusDurables 只放行 Storage View 的消费者（按数据集动态创建的 view_<12 位十六进制>），去重后返回，保持调用方给出的顺序。
func checkEventBusDurables(durables []string) ([]string, error) {
	if len(durables) == 0 {
		return nil, errors.New("至少指定一个消费者")
	}
	if len(durables) > maxEventBusTargets {
		return nil, fmt.Errorf("一次最多处理 %d 个消费者", maxEventBusTargets)
	}
	out := make([]string, 0, len(durables))
	for _, durable := range durables {
		if !events.IsStorageViewDurable(durable) {
			return nil, fmt.Errorf("不支持的消费者 %q，只允许按数据集动态创建的 view_<12位十六进制>", durable)
		}
		if !slices.Contains(out, durable) {
			out = append(out, durable)
		}
	}
	return out, nil
}

// checkEventBusSubjects 只放行数据集事件的确切主题，不接受通配符，去重后返回。
func checkEventBusSubjects(subjects []string) ([]string, error) {
	if len(subjects) == 0 {
		return nil, errors.New("至少指定一个主题")
	}
	if len(subjects) > maxEventBusTargets {
		return nil, fmt.Errorf("一次最多处理 %d 个主题", maxEventBusTargets)
	}
	out := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if !events.IsStorageDatasetSubject(subject) {
			return nil, fmt.Errorf("不支持的主题 %q，只允许某个空间某个数据集的数据事件主题（不含通配符）", subject)
		}
		if !slices.Contains(out, subject) {
			out = append(out, subject)
		}
	}
	return out, nil
}

// InspectEventBus 返回流里指定消费者是否存在及其过滤主题。流不存在时返回 NOT_FOUND。
func (s *ServiceImpl) InspectEventBus(ctx context.Context, req *pb.InspectEventBusReq) (*pb.InspectEventBusRsp, error) {
	if err := checkEventBusStream(req.GetStream()); err != nil {
		return &pb.InspectEventBusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	durables, err := checkEventBusDurables(req.GetDurables())
	if err != nil {
		return &pb.InspectEventBusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	session, err := s.openEventBusSession(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "eventbus ops inspect: %v", err)
		return &pb.InspectEventBusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	defer session.Close()
	streamCtx, cancelStream := eventBusRequest(ctx)
	_, err = session.StreamInfo(req.GetStream(), nats.Context(streamCtx))
	cancelStream()
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return &pb.InspectEventBusRsp{RetInfo: eventBusRet(pb.ErrorCode_NOT_FOUND, "EventBus 流 %s 不存在", req.GetStream())}, nil
		}
		return &pb.InspectEventBusRsp{RetInfo: eventBusRet(pb.ErrorCode_INNER_ERR, "读取 EventBus 流 %s 失败: %v", req.GetStream(), err)}, nil
	}
	rsp := &pb.InspectEventBusRsp{RetInfo: retOK()}
	for _, durable := range durables {
		consumerCtx, cancelConsumer := eventBusRequest(ctx)
		info, err := session.ConsumerInfo(req.GetStream(), durable, nats.Context(consumerCtx))
		cancelConsumer()
		if errors.Is(err, nats.ErrConsumerNotFound) {
			rsp.Consumers = append(rsp.Consumers, &pb.EventBusConsumerState{Durable: durable})
			continue
		}
		if err != nil {
			return &pb.InspectEventBusRsp{RetInfo: eventBusRet(pb.ErrorCode_INNER_ERR, "读取消费者 %s 失败: %v", durable, err)}, nil
		}
		rsp.Consumers = append(rsp.Consumers, &pb.EventBusConsumerState{
			Durable: durable, Exists: true,
			FilterSubjects: append([]string(nil), info.Config.FilterSubjects...), FilterSubject: info.Config.FilterSubject,
		})
	}
	return rsp, nil
}

// DeleteEventBusConsumers 删除流里指定的消费者；本来就不存在的跳过，不算失败，也不出现在 deleted 里。
func (s *ServiceImpl) DeleteEventBusConsumers(ctx context.Context, req *pb.DeleteEventBusConsumersReq) (*pb.DeleteEventBusConsumersRsp, error) {
	if err := checkEventBusStream(req.GetStream()); err != nil {
		return &pb.DeleteEventBusConsumersRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	durables, err := checkEventBusDurables(req.GetDurables())
	if err != nil {
		return &pb.DeleteEventBusConsumersRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	session, err := s.openEventBusSession(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "eventbus ops delete consumers: %v", err)
		return &pb.DeleteEventBusConsumersRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	defer session.Close()
	rsp := &pb.DeleteEventBusConsumersRsp{RetInfo: retOK()}
	for _, durable := range durables {
		rsp.Attempted = true
		removed, err := deleteConsumerWithRetry(ctx, session, req.GetStream(), durable)
		if !removed && err == nil {
			continue
		}
		if err != nil {
			// 已删掉的消费者仍然返回，调用方据此知道哪些已经动过。
			rsp.RetInfo = eventBusRet(pb.ErrorCode_INNER_ERR, "删除消费者 %s 失败: %v", durable, err)
			return rsp, nil
		}
		rsp.Deleted = append(rsp.Deleted, durable)
	}
	log.WarnContextf(ctx, "eventbus ops 已删除消费者 stream=%s deleted=%v", req.GetStream(), rsp.Deleted)
	return rsp, nil
}

// 服务端删除消费者目录时，消费者的状态文件可能刚好还在落盘，会返回“directory not empty”；
// 这是瞬时冲突，稍后重试即可。
const (
	eventBusDeleteAttempts = 5
	eventBusDeleteBackoff  = 100 * time.Millisecond
)

func isTransientConsumerDeleteError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "directory not empty")
}

// deleteConsumerWithRetry 删除一个消费者并返回是否由本次调用删掉：
// 消费者本来就不存在返回 (false, nil)；瞬时冲突后重试时发现已不存在，说明前一次其实已经删掉，返回 (true, nil)。
func deleteConsumerWithRetry(ctx context.Context, session eventBusSession, stream, durable string) (bool, error) {
	var err error
	conflicted := false
	for attempt := 0; attempt < eventBusDeleteAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(eventBusDeleteBackoff * time.Duration(attempt)):
			}
		}
		deleteCtx, cancel := eventBusRequest(ctx)
		err = session.DeleteConsumer(stream, durable, nats.Context(deleteCtx))
		cancel()
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, nats.ErrConsumerNotFound):
			return conflicted, nil
		case isTransientConsumerDeleteError(err):
			conflicted = true
		default:
			return false, err
		}
	}
	return false, err
}

// PurgeEventBusSubjects 清除流里指定主题的消息；主题必须是数据集事件的确切主题。
func (s *ServiceImpl) PurgeEventBusSubjects(ctx context.Context, req *pb.PurgeEventBusSubjectsReq) (*pb.PurgeEventBusSubjectsRsp, error) {
	if err := checkEventBusStream(req.GetStream()); err != nil {
		return &pb.PurgeEventBusSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	subjects, err := checkEventBusSubjects(req.GetSubjects())
	if err != nil {
		return &pb.PurgeEventBusSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	session, err := s.openEventBusSession(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "eventbus ops purge subjects: %v", err)
		return &pb.PurgeEventBusSubjectsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	defer session.Close()
	purged := 0
	for _, subject := range subjects {
		// nats.go 的 PurgeStream 不会把 Context 传给底层请求（只用 JetStream 的 MaxWait 限制单次等待），
		// 所以 RPC 超时或被取消后循环不会自己停下：这里在每个主题之前检查，已经没人在等结果时不再继续清除，
		// 否则调用方超时后重试并重启 Storage，旧请求还会清掉新产生的事件。
		if err := ctx.Err(); err != nil {
			log.ErrorContextf(ctx, "eventbus ops 清除中断 stream=%s purged=%d: %v", req.GetStream(), purged, err)
			return &pb.PurgeEventBusSubjectsRsp{RetInfo: eventBusRet(pb.ErrorCode_INNER_ERR, "请求已超时或被取消，停止清除（已清除 %d 个）: %v", purged, err), Attempted: purged > 0}, nil
		}
		purgeCtx, cancelPurge := eventBusRequest(ctx)
		err := session.PurgeStream(req.GetStream(), &nats.StreamPurgeRequest{Subject: subject}, nats.Context(purgeCtx))
		cancelPurge()
		if err != nil {
			log.ErrorContextf(ctx, "eventbus ops 清除主题失败 stream=%s subject=%s purged=%d: %v", req.GetStream(), subject, purged, err)
			return &pb.PurgeEventBusSubjectsRsp{RetInfo: eventBusRet(pb.ErrorCode_INNER_ERR, "清除主题 %s 失败（已清除 %d 个）: %v", subject, purged, err), Attempted: true}, nil
		}
		purged++
	}
	log.WarnContextf(ctx, "eventbus ops 已清除主题 stream=%s subjects=%d", req.GetStream(), purged)
	return &pb.PurgeEventBusSubjectsRsp{RetInfo: retOK(), Attempted: true}, nil
}
