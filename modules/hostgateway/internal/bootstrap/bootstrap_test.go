package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/controlplane"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/health"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/healthz"
)

func testSnapshot(t *testing.T, hostID, component string) *adminpb.HostSnapshot {
	t.Helper()
	built, err := testsnapshot.Build(hostID, false, []gatewayroute.Route{{
		ServiceID: component, Address: "127.0.0.1:1234", ServicePath: "trpc.moox.test." + component,
		AllowedMethods: []string{"Get"}, AllowedCallers: []string{"strategy"},
	}}, []gatewayroute.VerificationKey{{KeyID: "strategy-1", Caller: "strategy", Secret: "s"}}, testsnapshot.Directory(hostID, "trpc.moox.test."+component))
	if err != nil {
		t.Fatal(err)
	}
	return built
}

type fakeStore struct {
	load    *adminpb.HostSnapshot
	loadErr error
	saved   []*adminpb.HostSnapshot
	events  *[]string
}

func (s *fakeStore) Load() (*adminpb.HostSnapshot, error) {
	s.record("load")
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if s.load == nil {
		return nil, os.ErrNotExist
	}
	return s.load, nil
}

func (s *fakeStore) Save(snapshot *adminpb.HostSnapshot) error {
	s.record("save")
	s.saved = append(s.saved, snapshot)
	return nil
}

func (s *fakeStore) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

type fakeControl struct {
	pull      *adminpb.HostSnapshot
	pullErr   error
	reportErr error
	reports   []controlplane.Status
	events    *[]string
}

func (c *fakeControl) Pull(_ context.Context, currentHash string) (*adminpb.HostSnapshot, bool, error) {
	if c.events != nil {
		*c.events = append(*c.events, "pull:"+short(currentHash))
	}
	if c.pullErr != nil {
		return nil, false, c.pullErr
	}
	if c.pull.GetHash() == currentHash {
		return nil, false, nil
	}
	return c.pull, true, nil
}

func (c *fakeControl) Report(_ context.Context, status controlplane.Status) error {
	if c.events != nil {
		*c.events = append(*c.events, "report:"+short(status.AppliedHash))
	}
	c.reports = append(c.reports, status)
	return c.reportErr
}

func short(hash string) string {
	if len(hash) > 6 {
		return hash[:6]
	}
	return hash
}

func metrics(t *testing.T, state *health.State) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	state.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func TestInitializeLoadsCacheBeforeInitialPull(t *testing.T) {
	cached := testSnapshot(t, "storage", "cached")
	pulled := testSnapshot(t, "storage", "fresh")
	events := []string{}
	store := &fakeStore{load: cached, events: &events}
	control := &fakeControl{pull: pulled, events: &events}
	runtime := New(Options{HostID: "storage", Store: store, Control: control, Health: health.NewState()})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "load,pull:" + short(cached.GetHash()) + ",save,report:" + short(pulled.GetHash())
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	if runtime.Current().Hash() != pulled.GetHash() || !runtime.Current().Load().Table.HasService("trpc.moox.test.fresh") {
		t.Fatal("没有换上新快照")
	}
}

func TestInitializeKeepsCacheWhenControlIsDown(t *testing.T) {
	cached := testSnapshot(t, "storage", "cached")
	state := health.NewState()
	runtime := New(Options{HostID: "storage", Store: &fakeStore{load: cached}, Control: &fakeControl{pullErr: errors.New("admin down")}, Health: state})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatalf("有缓存时网关控制不可用也应当启动: %v", err)
	}
	if runtime.Current().Hash() != cached.GetHash() {
		t.Fatal("应当使用缓存的快照")
	}
	if !strings.Contains(metrics(t, state), "host_gateway_route_sync_errors_total 1") {
		t.Fatal("同步失败应当计数")
	}
}

func TestInitializeRequiresCacheOrSuccessfulPull(t *testing.T) {
	runtime := New(Options{HostID: "storage", Store: &fakeStore{}, Control: &fakeControl{pullErr: errors.New("admin down")}, Health: health.NewState()})
	if err := runtime.Initialize(context.Background()); err == nil {
		t.Fatal("没有缓存也拉不到快照时不能启动")
	}
}

func TestInvalidCacheAndSnapshotsAreRejected(t *testing.T) {
	other := testSnapshot(t, "compute-1", "other")
	state := health.NewState()
	control := &fakeControl{pull: testSnapshot(t, "storage", "fresh")}
	runtime := New(Options{HostID: "storage", Store: &fakeStore{load: other}, Control: control, Health: state, Warn: func(string) {}})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metrics(t, state), "host_gateway_route_validation_failures_total 1") {
		t.Fatal("无效缓存应当计入校验失败")
	}
	good := runtime.Current().Hash()
	tampered := testSnapshot(t, "storage", "tampered")
	tampered.Hash = "not-the-hash"
	control.pull = tampered
	if err := runtime.Refresh(context.Background()); err == nil {
		t.Fatal("被篡改的快照应当被拒绝")
	}
	if runtime.Current().Hash() != good {
		t.Fatal("拒绝新快照时应当继续使用旧快照")
	}
}

func TestRefreshUnchangedHashSkipsSave(t *testing.T) {
	snapshot := testSnapshot(t, "storage", "monitor")
	events := []string{}
	runtime := New(Options{HostID: "storage", Store: &fakeStore{load: snapshot, events: &events}, Control: &fakeControl{pull: snapshot, events: &events}, Health: health.NewState()})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = events[:0]
	if err := runtime.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "pull:"+short(snapshot.GetHash())+",report:"+short(snapshot.GetHash()) {
		t.Fatalf("events = %s", got)
	}
}

func TestReportCarriesCertificateExpiry(t *testing.T) {
	notAfter := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	control := &fakeControl{pull: testSnapshot(t, "storage", "monitor")}
	runtime := New(Options{HostID: "storage", Store: &fakeStore{}, Control: control, Health: health.NewState(), CertificateNotAfter: notAfter})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(control.reports) != 1 || !control.reports[0].CertificateNotAfter.Equal(notAfter) || control.reports[0].RouteCount != 1 {
		t.Fatalf("reports = %+v", control.reports)
	}
	control.pullErr = errors.New("拉取失败")
	_ = runtime.Refresh(context.Background())
	if last := control.reports[len(control.reports)-1]; !strings.Contains(last.LastError, "拉取失败") {
		t.Fatalf("心跳应当带上最近一次错误: %+v", last)
	}
}

func TestContinuousSyncFailureWarnsAfterThresholdAndResetsOnRecovery(t *testing.T) {
	snapshot := testSnapshot(t, "storage", "monitor")
	state := health.NewState()
	control := &fakeControl{pull: snapshot}
	now := time.Unix(1_700_000_000, 0)
	warnings := []string{}
	runtime := New(Options{
		HostID: "storage", Store: &fakeStore{load: snapshot}, Control: control, Health: state,
		Now: func() time.Time { return now }, Warn: func(message string) { warnings = append(warnings, message) },
		SyncWarningAfter: 10 * time.Minute, SyncWarningInterval: 10 * time.Minute,
	})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	control.pullErr = errors.New("admin unavailable")
	_ = runtime.Refresh(context.Background())
	now = now.Add(9 * time.Minute)
	_ = runtime.Refresh(context.Background())
	if len(warnings) != 0 {
		t.Fatalf("阈值之前不应告警: %v", warnings)
	}
	now = now.Add(time.Minute)
	_ = runtime.Refresh(context.Background())
	if len(warnings) != 1 || !strings.Contains(warnings[0], "host_id=storage") {
		t.Fatalf("达到阈值应当告警一次: %v", warnings)
	}
	if state.Ready() {
		t.Fatal("同步持续失败超过 90 秒应当未就绪")
	}
	if runtime.Current().Hash() != snapshot.GetHash() {
		t.Fatal("告警不能丢弃缓存的快照")
	}
	control.pullErr = nil
	if err := runtime.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !state.Ready() {
		t.Fatal("恢复后应当就绪")
	}
}

func TestReportFailureDegradesReadinessOnlyAfterStaleWindow(t *testing.T) {
	snapshot := testSnapshot(t, "storage", "monitor")
	state := health.NewState()
	control := &fakeControl{pull: snapshot}
	runtime := New(Options{HostID: "storage", Store: &fakeStore{load: snapshot}, Control: control, Health: state, Warn: func(string) {}})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	control.reportErr = errors.New("heartbeat down")
	if err := runtime.Refresh(context.Background()); err == nil {
		t.Fatal("心跳失败应当报错")
	}
	if !state.Ready() {
		t.Fatal("一次心跳失败不应立即降级")
	}
	if !strings.Contains(metrics(t, state), "host_gateway_route_report_errors_total 1") {
		t.Fatal("心跳失败应当计数")
	}
}

func TestAuthenticatedHealthHandlerRejectsUnsignedDiagnostics(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	handler, err := healthz.WrapFromEnv(health.NewState().Handler())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
	}
}
