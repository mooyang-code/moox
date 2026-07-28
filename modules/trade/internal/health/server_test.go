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
		configErrors    []string
		want            bool
	}{
		{
			name: "EventBus disabled and no live accounts",
			want: true,
		},
		{
			name: "all requirements ready", eventBusEnabled: true, eventBusReady: true,
			sessions: traderuntime.SessionSnapshot{EnabledLive: 2, Ready: 2},
			want:     true,
		},
		{
			name: "database unavailable", databaseErr: errors.New("database down"),
		},
		{
			name: "enabled EventBus unavailable", eventBusEnabled: true,
		},
		{
			name:     "one live session disconnected",
			sessions: traderuntime.SessionSnapshot{EnabledLive: 2, Ready: 1},
		},
		{
			name:         "configuration error",
			configErrors: []string{"invalid account"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readiness := Readiness{
				DatabaseReady:   func(context.Context) error { return tt.databaseErr },
				EventBusEnabled: tt.eventBusEnabled,
				EventBusReady:   func() bool { return tt.eventBusReady },
				Sessions:        healthSessions{snapshot: tt.sessions},
				ConfigErrors:    func() []string { return tt.configErrors },
			}
			ready, details := readiness.Evaluate(context.Background())
			require.Equal(t, tt.want, ready)
			require.Equal(t, tt.sessions.EnabledLive, details["enabled_live_exchange_accounts"])
		})
	}
}
