package unitwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

func writeEventBusCredential(t *testing.T, root, url string) {
	t.Helper()
	path := eventBusCredentialPath(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	raw := "version: 1\nurls: [" + url + "]\nusername: hostagent-publisher\neventbus_token: synthetic-token-at-least-32-bytes-long\nca_file: ca.pem\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
}

// EventBus 不在本机：用 HostAgent 凭据里的地址、主机名与 CA（相对路径按凭据文件所在目录解析）做握手；
// EventBus 在本机（回环地址）或没有凭据文件：没有这一项。
func TestCheckEventBusPeerProbesRemoteEndpointOnly(t *testing.T) {
	root := t.TempDir()
	probe := func(result error, calls *[]string) PeerProbe {
		return func(_ context.Context, target, serverName, caFile string) error {
			*calls = append(*calls, target+"|"+serverName+"|"+caFile)
			return result
		}
	}
	var calls []string

	report, err := checkEventBusPeer(t.Context(), root, probe(nil, &calls))
	require.NoError(t, err)
	require.Nil(t, report, "没有凭据文件")

	writeEventBusCredential(t, root, "tls://127.0.0.1:4222")
	report, err = checkEventBusPeer(t.Context(), root, probe(nil, &calls))
	require.NoError(t, err)
	require.Nil(t, report, "EventBus 在本机，由 eventbus 组件自己的就绪状态说明")
	require.Empty(t, calls)

	writeEventBusCredential(t, root, "tls://203.0.113.20:4222")
	report, err = checkEventBusPeer(t.Context(), root, probe(nil, &calls))
	require.NoError(t, err)
	require.True(t, report.Reachable)
	require.Equal(t, "203.0.113.20:4222", report.Target)
	require.Equal(t, []string{"203.0.113.20:4222|203.0.113.20|" + filepath.Join(filepath.Dir(eventBusCredentialPath(root)), "ca.pem")}, calls)

	report, err = checkEventBusPeer(t.Context(), root, probe(errors.New("i/o timeout"), &calls))
	require.NoError(t, err)
	require.False(t, report.Reachable)
	require.Equal(t, "i/o timeout", report.Error)
}

// 与 control 交叉检查同一套规则：连续三轮失败发一条严重告警，之后不重复；恢复时发一条提示；一次短暂失败不告警。
func TestObserveEventBusAlertsOnceAndRecovers(t *testing.T) {
	state := &peerState{}
	start := time.Date(2026, 10, 27, 6, 0, 3, 0, time.UTC)
	fail := &EventBusReport{Target: "203.0.113.20:4222", Error: "dial tcp: i/o timeout"}
	for i := 0; i < 2; i++ {
		require.Empty(t, observeEventBus(fail, state, "control", start.Add(time.Duration(i)*time.Minute)))
	}
	messages := observeEventBus(fail, state, "control", start.Add(2*time.Minute))
	require.Len(t, messages, 1)
	require.Equal(t, notification.SeverityCritical, messages[0].Severity)
	require.Equal(t, "【MooX 交叉检查】EventBus 不可达（由 control 观测）", messages[0].Title)
	require.Contains(t, messages[0].Body, "i/o timeout")
	require.Positive(t, fail.FailingSeconds)
	require.Empty(t, observeEventBus(fail, state, "control", start.Add(3*time.Minute)))

	ok := &EventBusReport{Target: fail.Target, Reachable: true}
	messages = observeEventBus(ok, state, "control", start.Add(5*time.Minute))
	require.Len(t, messages, 1)
	require.Equal(t, "【MooX 交叉检查】EventBus 已恢复（由 control 观测）", messages[0].Title)
	require.Empty(t, observeEventBus(ok, state, "control", start.Add(6*time.Minute)))

	require.Empty(t, observeEventBus(fail, state, "control", start.Add(7*time.Minute)))
	require.Empty(t, observeEventBus(ok, state, "control", start.Add(8*time.Minute)))
}

func notReady(component string) ComponentReport {
	return ComponentReport{Unit: "prod", Component: component, Outcome: OutcomeNotReady, NotReadySeconds: 3600, Ready: false}
}

// EventBus 不可用时，依赖它的组件都会未就绪：不再逐个报"持续未就绪"，Admin 与主机网关（不依赖 EventBus）仍照常报；
// EventBus 可用时所有组件照常报。
func TestPlanAlertsSuppressesNotReadyWhileEventBusIsDown(t *testing.T) {
	now := time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)
	policy := Policy{}.withDefaults()
	plan := func(report Report) []string {
		return titles(planAlerts(report, &watchState{}, now, policy))
	}
	components := []ComponentReport{notReady("strategy"), notReady("collector"), notReady("admin")}

	healthy := Report{HostID: "control", Components: components, EventBusPeer: &EventBusReport{Reachable: true}}
	require.Len(t, plan(healthy), 3)

	remoteDown := Report{HostID: "control", Components: components, EventBusPeer: &EventBusReport{Reachable: false}}
	require.Equal(t, []string{"【MooX 守护】control · admin · 持续未就绪"}, plan(remoteDown))

	// EventBus 在本机、broker 自己没有就绪：同样只剩 broker 与不依赖它的组件各自的告警。
	localDown := Report{HostID: "storage", Components: []ComponentReport{notReady("eventbus"), notReady("storage-view"), notReady("host-agent")}}
	require.Equal(t, []string{"【MooX 守护】storage · eventbus · 持续未就绪"}, plan(localDown))

	// 本机 broker 健康：照常报各组件。
	localUp := Report{HostID: "storage", Components: []ComponentReport{{Unit: "eventbus", Component: "eventbus", Outcome: OutcomeHealthy, Ready: true}, notReady("storage-view")}}
	require.Equal(t, []string{"【MooX 守护】storage · storage-view · 持续未就绪"}, plan(localUp))
}

// 整轮守护：EventBus 端点检查结果写进报告，状态跨轮保存在 peer-eventbus.json，只报一条。
func TestRunDeliversEventBusPeerAlertAndSuppressesNotReady(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"host": {"host-gateway", "host-agent"}}, hostpolicy.Watchdog{})
	writeEventBusCredential(t, h.root, "tls://203.0.113.20:4222")
	h.respond = func(string) (unitruntime.Result, error) {
		return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 5, Ready: false}}}, nil
	}
	for i := 0; i < 3; i++ {
		report := run()
		require.NotNil(t, report.EventBusPeer)
		require.False(t, report.EventBusPeer.Reachable, "本机没有 CA 文件，握手必然失败，不需要真实网络")
		h.now = h.now.Add(time.Minute)
	}
	require.Equal(t, []string{"【MooX 交叉检查】EventBus 不可达（由 control 观测）"}, titles(sink.messages))
	require.FileExists(t, filepath.Join(h.root, "run", "watchdog", "peer-eventbus.json"))
}
