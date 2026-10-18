package sysdeploy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/events"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// fakeEventBusSession 记录维护操作并按需返回错误。
type fakeEventBusSession struct {
	streamMissing   bool
	consumers       map[string]*nats.ConsumerInfo
	deleteErrors    map[string]error
	purgeErrors     map[string]error
	deleted, purged []string
	closed          bool
	// deadlines 记录每次请求所用 Context 的截止时间（无截止时间的记零值）。
	deadlines []time.Time
}

func (f *fakeEventBusSession) record(opts []nats.JSOpt) {
	var deadline time.Time
	for _, opt := range opts {
		if c, ok := opt.(nats.ContextOpt); ok {
			deadline, _ = c.Deadline()
		}
	}
	f.deadlines = append(f.deadlines, deadline)
}

func (f *fakeEventBusSession) StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	f.record(opts)
	if f.streamMissing {
		return nil, nats.ErrStreamNotFound
	}
	return &nats.StreamInfo{Config: nats.StreamConfig{Name: stream}}, nil
}

func (f *fakeEventBusSession) ConsumerInfo(_, durable string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	f.record(opts)
	if info, ok := f.consumers[durable]; ok {
		return info, nil
	}
	return nil, nats.ErrConsumerNotFound
}

func (f *fakeEventBusSession) DeleteConsumer(_, durable string, opts ...nats.JSOpt) error {
	f.record(opts)
	if err := f.deleteErrors[durable]; err != nil {
		return err
	}
	if _, ok := f.consumers[durable]; !ok {
		return nats.ErrConsumerNotFound
	}
	f.deleted = append(f.deleted, durable)
	return nil
}

func (f *fakeEventBusSession) PurgeStream(_ string, opts ...nats.JSOpt) error {
	f.record(opts)
	subject := ""
	for _, opt := range opts {
		if request, ok := opt.(*nats.StreamPurgeRequest); ok {
			subject = request.Subject
		}
	}
	if err := f.purgeErrors[subject]; err != nil {
		return err
	}
	f.purged = append(f.purged, subject)
	return nil
}

func (f *fakeEventBusSession) Close() { f.closed = true }

// eventBusTestService 返回用替身会话的服务，opened 统计打开会话的次数。
func eventBusTestService(session *fakeEventBusSession) (*ServiceImpl, *int) {
	opened := 0
	service := &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) {
		opened++
		return session, nil
	}}
	return service, &opened
}

func datasetSubjects(t *testing.T, spaceID, datasetID string) []string {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	subjects := make([]string, 0, len(events.StorageDatasetEvents))
	for _, event := range events.StorageDatasetEvents {
		subject, err := registry.RenderSubject(event, spaceID, datasetID)
		require.NoError(t, err)
		subjects = append(subjects, subject)
	}
	return subjects
}

func TestEventBusOpsRejectOutOfScopeRequestsBeforeConnecting(t *testing.T) {
	session := &fakeEventBusSession{}
	service, opened := eventBusTestService(session)
	ctx := t.Context()
	good := datasetSubjects(t, "mooxsys", "kline.1m")[0]
	tooMany := make([]string, maxEventBusTargets+1)
	for i := range tooMany {
		tooMany[i] = events.StorageViewFactorConsumer
	}

	for name, call := range map[string]func() *pb.RetInfo{
		"查看-别的流": func() *pb.RetInfo {
			rsp, _ := service.InspectEventBus(ctx, &pb.InspectEventBusReq{Stream: "MOOX_TRADE", Durables: []string{events.StorageViewFactorConsumer}})
			return rsp.GetRetInfo()
		},
		"查看-别的消费者": func() *pb.RetInfo {
			rsp, _ := service.InspectEventBus(ctx, &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: []string{"factor_collector_period_v1"}})
			return rsp.GetRetInfo()
		},
		"查看-未指定消费者": func() *pb.RetInfo {
			rsp, _ := service.InspectEventBus(ctx, &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream})
			return rsp.GetRetInfo()
		},
		"删除-别的流": func() *pb.RetInfo {
			rsp, _ := service.DeleteEventBusConsumers(ctx, &pb.DeleteEventBusConsumersReq{Stream: "MOOX_OBSERVABILITY", Durables: []string{events.StorageViewFactorConsumer}})
			return rsp.GetRetInfo()
		},
		"删除-通配消费者": func() *pb.RetInfo {
			rsp, _ := service.DeleteEventBusConsumers(ctx, &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: []string{">"}})
			return rsp.GetRetInfo()
		},
		"删除-数量超限": func() *pb.RetInfo {
			rsp, _ := service.DeleteEventBusConsumers(ctx, &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: tooMany})
			return rsp.GetRetInfo()
		},
		"清除-别的流": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: "MOOX_TRADE", Subjects: []string{good}})
			return rsp.GetRetInfo()
		},
		"清除-整条流": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: []string{">"}})
			return rsp.GetRetInfo()
		},
		"清除-事件族通配": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: []string{"moox.event.storage.dataset.rows.upserted.v2.>"}})
			return rsp.GetRetInfo()
		},
		"清除-其他事件": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: []string{"moox.event.observability.host.snapshot.reported.v1.mfrxg4tt.mfrxg4tt"}})
			return rsp.GetRetInfo()
		},
		"清除-没有主题": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream})
			return rsp.GetRetInfo()
		},
		"清除-混入越界主题": func() *pb.RetInfo {
			rsp, _ := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: []string{good, "moox.>"}})
			return rsp.GetRetInfo()
		},
	} {
		t.Run(name, func(t *testing.T) {
			ret := call()
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, ret.GetCode(), ret.GetMsg())
			require.NotEmpty(t, ret.GetMsg())
		})
	}
	require.Zero(t, *opened, "越界请求不应打开 EventBus 连接")
	require.Empty(t, session.deleted)
	require.Empty(t, session.purged)
}

func TestEventBusOpsAcceptDynamicViewConsumers(t *testing.T) {
	dynamic := []string{"storage_view_misc_0123456789ab", "storage_view_factor_ffffffffffff"}
	session := &fakeEventBusSession{consumers: map[string]*nats.ConsumerInfo{dynamic[0]: {}, dynamic[1]: {}}}
	service, _ := eventBusTestService(session)
	rsp, err := service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: dynamic})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, dynamic, rsp.GetDeleted(), "按数据集动态创建的 View 消费者可以单独删除（force-rebuild-view 需要）")

	for _, name := range []string{"storage_view_misc_*", "storage_view_misc_0123456789a", "storage_view_misc_0123456789AB"} {
		rsp, err := service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: []string{name}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
	}
}

func TestInspectEventBusReportsConsumersAndMissingStream(t *testing.T) {
	session := &fakeEventBusSession{consumers: map[string]*nats.ConsumerInfo{
		events.StorageViewFactorConsumer:  {Config: nats.ConsumerConfig{FilterSubjects: []string{"a.b", "c.d"}}},
		events.StorageViewMetricsConsumer: {Config: nats.ConsumerConfig{FilterSubject: "legacy.single"}},
	}}
	service, _ := eventBusTestService(session)
	req := &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer, events.StorageViewMiscConsumer, events.StorageViewFactorConsumer}}

	rsp, err := service.InspectEventBus(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, session.closed)
	require.Len(t, rsp.GetConsumers(), 3, "重复的消费者只查一次")
	factor, metrics, misc := rsp.GetConsumers()[0], rsp.GetConsumers()[1], rsp.GetConsumers()[2]
	require.Equal(t, events.StorageViewFactorConsumer, factor.GetDurable())
	require.True(t, factor.GetExists())
	require.Equal(t, []string{"a.b", "c.d"}, factor.GetFilterSubjects())
	require.True(t, metrics.GetExists())
	require.Equal(t, "legacy.single", metrics.GetFilterSubject())
	require.False(t, misc.GetExists())
	require.Empty(t, misc.GetFilterSubjects())

	session.streamMissing = true
	rsp, err = service.InspectEventBus(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), events.StorageViewConsumerStream)
}

func TestDeleteEventBusConsumersSkipsMissingAndReportsPartialFailure(t *testing.T) {
	session := &fakeEventBusSession{consumers: map[string]*nats.ConsumerInfo{
		events.StorageViewFactorConsumer:  {},
		events.StorageViewMetricsConsumer: {},
	}}
	service, _ := eventBusTestService(session)

	rsp, err := service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{
		Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer}, rsp.GetDeleted(), "不存在的 misc 不计入")
	require.True(t, rsp.GetAttempted())
	require.True(t, session.closed)

	session = &fakeEventBusSession{
		consumers:    map[string]*nats.ConsumerInfo{events.StorageViewFactorConsumer: {}, events.StorageViewMetricsConsumer: {}, events.StorageViewMiscConsumer: {}},
		deleteErrors: map[string]error{events.StorageViewMetricsConsumer: errors.New("boom")},
	}
	service, _ = eventBusTestService(session)
	rsp, err = service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{
		Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), events.StorageViewMetricsConsumer)
	require.Equal(t, []string{events.StorageViewFactorConsumer}, rsp.GetDeleted(), "失败前已删除的要返回，调用方才知道动过什么")
	require.True(t, rsp.GetAttempted())
	require.NotContains(t, session.deleted, events.StorageViewMiscConsumer, "出错后不再继续删除")
}

func TestPurgeEventBusSubjectsPurgesEachSubjectOnceAndStopsOnFailure(t *testing.T) {
	subjects := datasetSubjects(t, "mooxsys", "kline.1m")
	session := &fakeEventBusSession{}
	service, _ := eventBusTestService(session)

	rsp, err := service.PurgeEventBusSubjects(t.Context(), &pb.PurgeEventBusSubjectsReq{
		Stream: events.StorageViewConsumerStream, Subjects: append(append([]string(nil), subjects...), subjects[0]),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, subjects, session.purged, "重复主题只清一次，顺序保持")
	require.True(t, rsp.GetAttempted())
	require.True(t, session.closed)

	session = &fakeEventBusSession{purgeErrors: map[string]error{subjects[2]: errors.New("boom")}}
	service, _ = eventBusTestService(session)
	rsp, err = service.PurgeEventBusSubjects(t.Context(), &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), subjects[2])
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "已清除 2 个")
	require.True(t, rsp.GetAttempted())
	require.Equal(t, subjects[:2], session.purged)
}

// 失败发生在发出变更请求之前（参数越界、连不上 EventBus）时 attempted 必须是 false：
// 调用方据此判断 EventBus 上什么都没动，可以安全地恢复本地状态。
func TestEventBusOpsReportNotAttemptedWhenFailingBeforeAnyMutation(t *testing.T) {
	unreachable := &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) {
		return nil, errors.New("连接 EventBus 失败")
	}}
	deleted, err := unreachable.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, deleted.GetRetInfo().GetCode())
	require.False(t, deleted.GetAttempted())
	purged, err := unreachable.PurgeEventBusSubjects(t.Context(), &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: datasetSubjects(t, "mooxsys", "ds")})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, purged.GetRetInfo().GetCode())
	require.False(t, purged.GetAttempted())

	service, _ := eventBusTestService(&fakeEventBusSession{})
	deleted, err = service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{Stream: "MOOX_TRADE", Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, deleted.GetRetInfo().GetCode())
	require.False(t, deleted.GetAttempted())
	purged, err = service.PurgeEventBusSubjects(t.Context(), &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: []string{">"}})
	require.NoError(t, err)
	require.False(t, purged.GetAttempted())
}

// nats.go 对带截止时间的 Context 会忽略 JetStream 的 MaxWait，所以每次请求必须自带两分钟上限；
// 否则一个无响应的请求会耗尽整个 RPC 的截止时间，已完成的删除与清除结果也来不及返回。
func TestEventBusOpsBoundEveryRequestIndependentlyOfTheRPCDeadline(t *testing.T) {
	subjects := datasetSubjects(t, "mooxsys", "kline.1m")
	session := &fakeEventBusSession{consumers: map[string]*nats.ConsumerInfo{events.StorageViewFactorConsumer: {}}}
	service, _ := eventBusTestService(session)

	long, cancelLong := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancelLong()
	_, err := service.InspectEventBus(long, &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	_, err = service.DeleteEventBusConsumers(long, &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	_, err = service.PurgeEventBusSubjects(long, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	require.Len(t, session.deadlines, 1+3+3+len(subjects), "每次 NATS 请求都要记录到")
	for i, deadline := range session.deadlines {
		require.False(t, deadline.IsZero(), "第 %d 次请求没有截止时间", i)
		require.LessOrEqual(t, time.Until(deadline), eventBusRequestTimeout, "第 %d 次请求的上限超过两分钟", i)
		require.Greater(t, time.Until(deadline), time.Minute, "第 %d 次请求的上限不应被误缩短", i)
	}

	// RPC 自己的截止时间更短时仍以它为准。
	short, cancelShort := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancelShort()
	session.deadlines = nil
	_, err = service.PurgeEventBusSubjects(short, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	for _, deadline := range session.deadlines {
		require.LessOrEqual(t, time.Until(deadline), 20*time.Second)
	}
}

// nats.go 的 PurgeStream 不转发 Context：RPC 被取消后如果循环不检查，还会继续清除剩余主题。
func TestPurgeEventBusSubjectsStopsOnceTheRPCIsCancelled(t *testing.T) {
	subjects := datasetSubjects(t, "mooxsys", "kline.1m")
	ctx, cancel := context.WithCancel(t.Context())
	session := &cancellingSession{fakeEventBusSession: &fakeEventBusSession{}, cancelAfter: 2, cancel: cancel}
	service := &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) { return session, nil }}

	rsp, err := service.PurgeEventBusSubjects(ctx, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "已清除 2 个")
	require.True(t, rsp.GetAttempted())
	require.Equal(t, subjects[:2], session.purged, "取消之后不能再清除剩余主题")

	// 一开始就已取消：什么都没做，也不算尝试过。
	done, cancelDone := context.WithCancel(t.Context())
	cancelDone()
	fresh := &fakeEventBusSession{}
	service = &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) { return fresh, nil }}
	rsp, err = service.PurgeEventBusSubjects(done, &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetAttempted())
	require.Empty(t, fresh.purged)
}

// cancellingSession 在第 cancelAfter 次清除之后取消 RPC 的 Context，模拟调用方等不及了。
type cancellingSession struct {
	*fakeEventBusSession
	cancelAfter int
	cancel      context.CancelFunc
}

func (c *cancellingSession) PurgeStream(stream string, opts ...nats.JSOpt) error {
	err := c.fakeEventBusSession.PurgeStream(stream, opts...)
	if len(c.purged) == c.cancelAfter {
		c.cancel()
	}
	return err
}

func TestEventBusOpsReportConnectionFailureWithoutLeakingCredentials(t *testing.T) {
	service := &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) {
		return nil, errors.New("连接 EventBus tls://127.0.0.1:4222 失败: refused")
	}}
	rsp, err := service.InspectEventBus(t.Context(), &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: []string{events.StorageViewFactorConsumer}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "连接 EventBus")
}

func seedEventBusSecret(t *testing.T, dao *secretdao.SecretDAO, keyID, secretType, value string) {
	t.Helper()
	require.NoError(t, dao.Create(t.Context(), &model.Secret{
		SpaceID: "mooxsys", SecretID: uuid.New().String(), Name: keyID, Category: "eventbus", Provider: "moox_eventbus",
		SecretType: secretType, KeyID: keyID, SecretValue: value, ExtraConfig: `{}`,
	}))
}

func TestResolveEventBusTargetUsesLoopbackOnlyOnTheEventBusHost(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "test-encryption-key-for-eventbus-ops")
	db := setupTopologyTestDB(t)
	topology, err := NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, topology.SyncHosts(t.Context(), []HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host", "eventbus"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-view"}},
	}))
	service := &ServiceImpl{db: db, adminNodeID: "control"}

	_, err = service.resolveEventBusTarget(t.Context())
	require.ErrorContains(t, err, "尚未生成", "没有令牌与 CA 时要说明原因")

	secrets := secretdao.NewSecretDAO(db)
	seedEventBusSecret(t, secrets, "eventbus_internal_admin", "token", "admin-token")
	seedEventBusSecret(t, secrets, "eventbus_storage", "token", "other-role-token")
	_, err = service.resolveEventBusTarget(t.Context())
	require.ErrorContains(t, err, "尚未生成", "只有令牌没有 CA 也不行")
	seedEventBusSecret(t, secrets, "eventbus_tls_ca", "certificate", "ca-pem")

	target, err := service.resolveEventBusTarget(t.Context())
	require.NoError(t, err)
	require.Equal(t, "tls://127.0.0.1:4222", target.address, "EventBus 与 Admin 同机时走回环")
	require.Equal(t, "admin-token", target.token, "必须是内部管理员令牌，不能取到其他角色的令牌")
	require.Equal(t, "ca-pem", target.caPEM)

	require.NoError(t, topology.SyncHosts(t.Context(), []HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host"}},
		{HostID: "bus", Address: "bus.example.test", Components: []string{"eventbus"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-view"}},
	}))
	target, err = service.resolveEventBusTarget(t.Context())
	require.NoError(t, err)
	require.Equal(t, "tls://bus.example.test:4222", target.address, "EventBus 在别的主机时走对外地址")
}

// tlsEventBus 启动一个与线上同样配置方式的 EventBus：TLS 握手优先、内部管理员只有 $JS.API 的发布权限。
func tlsEventBus(t *testing.T) eventBusTarget {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "moox test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "eventbus"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	require.NoError(t, err)

	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}}, MinVersion: tls.VersionTLS12},
		TLSHandshakeFirst: true,
		Users: []*natsserver.User{
			{
				Username: eventBusAdminRole, Password: "admin-token",
				Permissions: &natsserver.Permissions{
					Publish:   &natsserver.SubjectPermission{Allow: []string{"$JS.API.>"}},
					Subscribe: &natsserver.SubjectPermission{Allow: []string{"_INBOX.>", "$JS.EVENT.ADVISORY.API"}},
				},
			},
			// 测试账号：只用来预置流、消费者和消息，服务本身不会用它。
			{Username: "seed", Password: "seed-token"},
		},
	})
	require.NoError(t, err)
	go server.Start()
	t.Cleanup(server.Shutdown)
	require.True(t, server.ReadyForConnections(10*time.Second))
	_, port, err := net.SplitHostPort(server.Addr().String())
	require.NoError(t, err)
	_, err = strconv.Atoi(port)
	require.NoError(t, err)
	return eventBusTarget{
		address: "tls://" + net.JoinHostPort("127.0.0.1", port),
		token:   "admin-token",
		caPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
	}
}

// 用真实 TLS 的 JetStream 走通：管理员令牌、内存里的 CA、握手优先、只有 $JS.API 权限都要够用。
func TestEventBusOpsAgainstTLSEventBus(t *testing.T) {
	target := tlsEventBus(t)
	service := &ServiceImpl{openEventBus: func(context.Context) (eventBusSession, error) { return connectEventBus(target) }}

	seed := seedConnection(t, target)
	session, err := seed.JetStream()
	require.NoError(t, err)
	_, err = session.AddStream(&nats.StreamConfig{Name: events.StorageViewConsumerStream, Subjects: []string{"moox.event.storage.>"}})
	require.NoError(t, err)
	subjects := datasetSubjects(t, "mooxsys", "kline.1m")
	for _, durable := range []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer} {
		_, err = session.AddConsumer(events.StorageViewConsumerStream, &nats.ConsumerConfig{Durable: durable, FilterSubjects: subjects[:2], AckPolicy: nats.AckExplicitPolicy})
		require.NoError(t, err)
	}
	for _, subject := range subjects {
		for i := 0; i < 3; i++ {
			_, err = session.Publish(subject, []byte("x"))
			require.NoError(t, err)
		}
	}
	other := datasetSubjects(t, "mooxsys", "kline.5m")[0]
	_, err = session.Publish(other, []byte("keep"))
	require.NoError(t, err)

	inspect, err := service.InspectEventBus(t.Context(), &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, inspect.GetRetInfo().GetCode(), inspect.GetRetInfo().GetMsg())
	require.Len(t, inspect.GetConsumers(), 3)
	require.True(t, inspect.GetConsumers()[0].GetExists())
	require.ElementsMatch(t, subjects[:2], inspect.GetConsumers()[0].GetFilterSubjects())
	require.False(t, inspect.GetConsumers()[2].GetExists(), "misc 没有创建")

	purge, err := service.PurgeEventBusSubjects(t.Context(), &pb.PurgeEventBusSubjectsReq{Stream: events.StorageViewConsumerStream, Subjects: subjects})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, purge.GetRetInfo().GetCode(), purge.GetRetInfo().GetMsg())
	info, err := session.StreamInfo(events.StorageViewConsumerStream, &nats.StreamInfoRequest{SubjectsFilter: ">"})
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs, "只清除指定数据集的消息，其他数据集的保留")
	require.Equal(t, 1, int(info.State.Subjects[other]))

	deleted, err := service.DeleteEventBusConsumers(t.Context(), &pb.DeleteEventBusConsumersReq{Stream: events.StorageViewConsumerStream, Durables: events.StorageViewConsumerDurables})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode(), deleted.GetRetInfo().GetMsg())
	require.Equal(t, []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer}, deleted.GetDeleted())

	// 流不存在 → NOT_FOUND，而不是笼统的内部错误。
	require.NoError(t, session.DeleteStream(events.StorageViewConsumerStream))
	rsp, err := service.InspectEventBus(t.Context(), &pb.InspectEventBusReq{Stream: events.StorageViewConsumerStream, Durables: []string{events.StorageViewFactorConsumer}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

// seedConnection 以测试账号连上 tlsEventBus，用来预置数据。
func seedConnection(t *testing.T, target eventBusTarget) *nats.Conn {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(target.caPEM)))
	conn, err := nats.Connect(target.address, nats.UserInfo("seed", "seed-token"),
		nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}), nats.TLSHandshakeFirst())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn
}

func TestConnectEventBusRejectsWrongTokenAndUntrustedCA(t *testing.T) {
	target := tlsEventBus(t)

	wrong := target
	wrong.token = "not-the-token"
	_, err := connectEventBus(wrong)
	require.ErrorContains(t, err, "连接 EventBus")
	require.NotContains(t, err.Error(), "admin-token")

	untrusted := tlsEventBus(t)
	mixed := target
	mixed.caPEM = untrusted.caPEM
	_, err = connectEventBus(mixed)
	require.ErrorContains(t, err, "连接 EventBus", "CA 不匹配时不能连上")

	bad := target
	bad.caPEM = "not pem"
	_, err = connectEventBus(bad)
	require.ErrorContains(t, err, "TLS CA")
}
