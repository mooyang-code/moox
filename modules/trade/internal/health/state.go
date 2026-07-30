package health

import (
	"context"
	"time"

	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/healthz"
)

type State = healthz.State

func New(module, instance, version, commit string) *State {
	return healthz.NewState(module, instance, version, commit)
}

type SessionSource interface {
	Snapshot() traderuntime.SessionSnapshot
}

type Readiness struct {
	DatabaseReady        func(context.Context) error
	EventBusEnabled      bool
	EventBusReady        func() bool
	Sessions             SessionSource
	LogicalAccountWorker func() (bool, string)
	TargetWorker         func() traderuntime.TargetWorkerSnapshot
	OperatorWorker       func() traderuntime.OperatorWorkerSnapshot
	ConfigErrors         func() []string
}

func (r Readiness) Evaluate(ctx context.Context) (bool, map[string]any) {
	databaseReady := r.DatabaseReady != nil && r.DatabaseReady(ctx) == nil
	eventBusReady := !r.EventBusEnabled
	if r.EventBusEnabled && r.EventBusReady != nil {
		eventBusReady = r.EventBusReady()
	}
	sessionSnapshot := traderuntime.SessionSnapshot{}
	if r.Sessions != nil {
		sessionSnapshot = r.Sessions.Snapshot()
	}
	configErrors := append([]string(nil), sessionSnapshot.ConfigErrors...)
	if r.ConfigErrors != nil {
		configErrors = append(configErrors, r.ConfigErrors()...)
	}
	sessionsReady := sessionSnapshot.Enabled > 0 &&
		sessionSnapshot.Reconciled &&
		sessionSnapshot.Ready == sessionSnapshot.Enabled
	targetWorker := traderuntime.TargetWorkerSnapshot{}
	if r.TargetWorker != nil {
		targetWorker = r.TargetWorker()
	}
	operatorWorker := traderuntime.OperatorWorkerSnapshot{}
	if r.OperatorWorker != nil {
		operatorWorker = r.OperatorWorker()
	}
	logicalAccountWorkerReady := false
	logicalAccountWorkerError := ""
	if r.LogicalAccountWorker != nil {
		logicalAccountWorkerReady, logicalAccountWorkerError =
			r.LogicalAccountWorker()
	}
	ready := databaseReady && eventBusReady && sessionsReady &&
		logicalAccountWorkerReady && targetWorker.Ready &&
		operatorWorker.Ready && len(configErrors) == 0
	return ready, map[string]any{
		"database_ready":               databaseReady,
		"eventbus_enabled":             r.EventBusEnabled,
		"eventbus_ready":               eventBusReady,
		"enabled_exchange_accounts":    sessionSnapshot.Enabled,
		"ready_exchange_sessions":      sessionSnapshot.Ready,
		"exchange_accounts_reconciled": sessionSnapshot.Reconciled,
		"logical_account_worker_ready": logicalAccountWorkerReady,
		"logical_account_worker_error": logicalAccountWorkerError,
		"target_worker_ready":          targetWorker.Ready,
		"target_worker_error":          targetWorker.LastError,
		"operator_worker_ready":        operatorWorker.Ready,
		"operator_worker_error":        operatorWorker.LastError,
		"configuration_errors":         configErrors,
	}
}

func SnapshotFunc(
	state *State,
	readiness Readiness,
	module string,
	instance string,
	version string,
	commit string,
	startedAt time.Time,
) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		ready, details := readiness.Evaluate(ctx)
		state.SetReady(ready)
		response := healthz.Base(module, instance, version, commit, startedAt, ready)
		response.Details = details
		return response
	}
}
