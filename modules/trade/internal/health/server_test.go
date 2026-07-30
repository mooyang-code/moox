package health

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type healthSessions struct {
	snapshot traderuntime.SessionSnapshot
}

func (s healthSessions) Snapshot() traderuntime.SessionSnapshot { return s.snapshot }

func TestState_New_ShouldInitializeModuleMetadata(t *testing.T) {
	state := New("trade", "instance-1", "v1", "abc")
	require.NotNil(t, state)
	assert.Equal(t, "trade", state.Module)
	assert.False(t, state.Ready())
}

func TestHandler_ReadinessEndpoint_NotReady_ShouldReturn503(t *testing.T) {
	state := New("trade", "test", "dev", "local")
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 503, rec.Code)
}

func TestHandler_ReadinessEndpoint_Ready_ShouldReturn200(t *testing.T) {
	state := New("trade", "test", "dev", "local")
	state.SetReady(true)
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 200, rec.Code)
}

func TestRegister_NilService_ShouldReturnError(t *testing.T) {
	state := New("trade", "test", "dev", "local")
	err := Register(nil, state)
	require.Error(t, err)
}

func TestHandler_MetricsEndpoint_ShouldExposePrometheusMetrics(t *testing.T) {
	state := New("trade", "test", "dev", "local")
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, 200, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "# HELP") || strings.Contains(rec.Body.String(), "# TYPE"))
}

func TestReadinessRequiresDatabaseEventBusAllLiveSessionsAndValidConfig(t *testing.T) {
	tests := []struct {
		name            string
		databaseErr     error
		eventBusEnabled bool
		eventBusReady   bool
		sessions        traderuntime.SessionSnapshot
		logicalReady    bool
		targetReady     bool
		operatorReady   bool
		configErrors    []string
		want            bool
	}{
		{
			name:         "EventBus disabled and initial account enumeration succeeded",
			sessions:     traderuntime.SessionSnapshot{Reconciled: true},
			logicalReady: true, targetReady: true, operatorReady: true,
			want: true,
		},
		{
			name: "all requirements ready", eventBusEnabled: true, eventBusReady: true,
			sessions:     traderuntime.SessionSnapshot{Enabled: 2, Ready: 2, Reconciled: true},
			logicalReady: true, targetReady: true, operatorReady: true,
			want: true,
		},
		{
			name: "account enumeration has never succeeded",
		},
		{
			name: "database unavailable", databaseErr: errors.New("database down"),
		},
		{
			name: "enabled EventBus unavailable", eventBusEnabled: true,
		},
		{
			name:     "one live session disconnected",
			sessions: traderuntime.SessionSnapshot{Enabled: 2, Ready: 1, Reconciled: true},
		},
		{
			name:         "configuration error",
			sessions:     traderuntime.SessionSnapshot{Reconciled: true},
			logicalReady: true, targetReady: true, operatorReady: true,
			configErrors: []string{"invalid account"},
		},
		{
			name:          "logical account worker has not recovered",
			sessions:      traderuntime.SessionSnapshot{Reconciled: true},
			targetReady:   true,
			operatorReady: true,
		},
		{
			name:          "target worker has not recovered",
			sessions:      traderuntime.SessionSnapshot{Reconciled: true},
			logicalReady:  true,
			operatorReady: true,
		},
		{
			name:         "operator worker has not recovered",
			sessions:     traderuntime.SessionSnapshot{Reconciled: true},
			logicalReady: true,
			targetReady:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readiness := Readiness{
				DatabaseReady:   func(context.Context) error { return tt.databaseErr },
				EventBusEnabled: tt.eventBusEnabled,
				EventBusReady:   func() bool { return tt.eventBusReady },
				Sessions:        healthSessions{snapshot: tt.sessions},
				LogicalAccountWorker: func() (bool, string) {
					return tt.logicalReady, ""
				},
				TargetWorker: func() traderuntime.TargetWorkerSnapshot {
					return traderuntime.TargetWorkerSnapshot{Ready: tt.targetReady}
				},
				OperatorWorker: func() traderuntime.OperatorWorkerSnapshot {
					return traderuntime.OperatorWorkerSnapshot{Ready: tt.operatorReady}
				},
				ConfigErrors: func() []string { return tt.configErrors },
			}
			ready, details := readiness.Evaluate(context.Background())
			require.Equal(t, tt.want, ready)
			require.Equal(t, tt.sessions.Enabled, details["enabled_exchange_accounts"])
			require.Equal(t, tt.logicalReady, details["logical_account_worker_ready"])
			require.Equal(t, tt.targetReady, details["target_worker_ready"])
			require.Equal(t, tt.operatorReady, details["operator_worker_ready"])
		})
	}
}
