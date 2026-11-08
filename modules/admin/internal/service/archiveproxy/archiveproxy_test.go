package archiveproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/stretchr/testify/require"
)

type fakeTopology struct {
	hosts       []sysdeploy.HostRecord
	deployments []sysdeploy.DeploymentRecord
}

func (f fakeTopology) Read(context.Context) ([]sysdeploy.HostRecord, []sysdeploy.DeploymentRecord, error) {
	return f.hosts, f.deployments, nil
}

// fakeRelay 把中转流接到伪造的 worker，记下目标主机与端口。
type fakeRelay struct {
	address string
	mu      sync.Mutex
	targets []string
}

func (f *fakeRelay) DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error) {
	f.mu.Lock()
	f.targets = append(f.targets, hostID+":"+strconv.Itoa(port))
	f.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.address)
}

// fakeWorker 是伪造的 worker 数据端口：记下收到的请求，回固定的头与正文。
func fakeWorker(t *testing.T) (*httptest.Server, *[]*http.Request) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/partition":
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Length", "5")
			w.Header().Set("Content-Disposition", `attachment; filename="dir_2026-10-15.csv.gz"`)
			w.Header().Set("X-Moox-SHA256", "abc")
			w.Header().Set("X-Internal", "secret")
			_, _ = w.Write([]byte("gzip!"))
		case "/v1/recent":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"msg":"远端副本未就绪"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func topology(workerHost, reachability string) fakeTopology {
	return fakeTopology{
		hosts: []sysdeploy.HostRecord{
			{HostID: "control", Status: "enabled"},
			{HostID: "factor", Status: "enabled", Reachability: "outbound"},
			{HostID: "storage", Status: "enabled", Reachability: reachability},
		},
		deployments: []sysdeploy.DeploymentRecord{
			{HostID: "control", ComponentID: "admin", Status: "enabled"},
			{HostID: workerHost, ComponentID: Component, Status: "enabled"},
		},
	}
}

func newProxy(t *testing.T, topo fakeTopology, workerAddr string) (*Proxy, *fakeRelay, *[]string) {
	relay := &fakeRelay{address: workerAddr}
	var local []string
	p, err := New(Options{Topology: topo, Relay: relay, ControlHostID: "control", DialLocal: func(ctx context.Context, address string) (net.Conn, error) {
		local = append(local, address)
		return (&net.Dialer{}).DialContext(ctx, "tcp", workerAddr)
	}})
	require.NoError(t, err)
	return p, relay, &local
}

func TestDownloadOnControlDialsLoopbackAndPassesHeaders(t *testing.T) {
	worker, seen := fakeWorker(t)
	p, relay, local := newProxy(t, topology("control", ""), worker.Listener.Addr().String())
	rec := httptest.NewRecorder()
	p.PartitionDownload(rec, httptest.NewRequest(http.MethodGet, "/api/admin/archive/PartitionDownload?ticket=t-1&space=crypto&dataset=ds&partition=2026-10-15&extra=x", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip!", rec.Body.String())
	require.Equal(t, "application/gzip", rec.Header().Get("Content-Type"))
	require.Equal(t, "5", rec.Header().Get("Content-Length"))
	require.Equal(t, `attachment; filename="dir_2026-10-15.csv.gz"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "abc", rec.Header().Get("X-Moox-SHA256"))
	require.Empty(t, rec.Header().Get("X-Internal"))
	require.Equal(t, []string{"127.0.0.1:11428"}, *local)
	require.Empty(t, relay.targets)
	// 只转发白名单参数，ticket 不发给 worker。
	require.Len(t, *seen, 1)
	got := (*seen)[0].URL.Query()
	require.Equal(t, "crypto", got.Get("space"))
	require.Equal(t, "2026-10-15", got.Get("partition"))
	require.Empty(t, got.Get("ticket"))
	require.Empty(t, got.Get("extra"))
}

func TestRecentOnOutboundHostUsesRelayAndPassesStatus(t *testing.T) {
	worker, seen := fakeWorker(t)
	p, relay, local := newProxy(t, topology("factor", ""), worker.Listener.Addr().String())
	rec := httptest.NewRecorder()
	p.RecentRows(rec, httptest.NewRequest(http.MethodGet, "/api/admin/archive/RecentRows?ticket=t&space=crypto&dataset=ds&subject=BTC&before=2026-10-15T00:00:00Z&limit=10", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	require.Contains(t, string(body), "远端副本未就绪")
	require.Equal(t, []string{"factor:11428"}, relay.targets)
	require.Empty(t, *local)
	got := (*seen)[0].URL.Query()
	require.Equal(t, "BTC", got.Get("subject"))
	require.Equal(t, "10", got.Get("limit"))
	require.Equal(t, "2026-10-15T00:00:00Z", got.Get("before"))
}

func TestOtherHostsAndMissingDeploymentAreRejected(t *testing.T) {
	worker, seen := fakeWorker(t)
	p, relay, local := newProxy(t, topology("storage", ""), worker.Listener.Addr().String())
	rec := httptest.NewRecorder()
	p.PartitionDownload(rec, httptest.NewRequest(http.MethodGet, "/x?space=crypto&dataset=ds&partition=2026-10-15", nil))
	require.Equal(t, http.StatusNotImplemented, rec.Code)
	require.Contains(t, rec.Body.String(), "只支持 control 与只出站主机")

	disabled := topology("factor", "")
	disabled.deployments[1].Status = "disabled"
	p, _, _ = newProxy(t, disabled, worker.Listener.Addr().String())
	rec = httptest.NewRecorder()
	p.RecentRows(rec, httptest.NewRequest(http.MethodGet, "/x?space=crypto&dataset=ds&subject=BTC", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "没有启用的归档执行器")

	hostOff := topology("factor", "")
	hostOff.hosts[1].Status = "disabled"
	p, _, _ = newProxy(t, hostOff, worker.Listener.Addr().String())
	rec = httptest.NewRecorder()
	p.RecentRows(rec, httptest.NewRequest(http.MethodGet, "/x?space=crypto&dataset=ds&subject=BTC", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	require.Empty(t, *seen)
	require.Empty(t, relay.targets)
	require.Empty(t, *local)
}

func TestWorkerUnreachableReturnsBadGateway(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	p, _, _ := newProxy(t, topology("control", ""), address)
	rec := httptest.NewRecorder()
	p.PartitionDownload(rec, httptest.NewRequest(http.MethodGet, "/x?space=crypto&dataset=ds&partition=2026-10-15", nil))
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "连接归档执行器失败")
}
