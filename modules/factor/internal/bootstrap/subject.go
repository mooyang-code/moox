package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
)

// StartEngineSubject no longer consumes ViewSourceSubjectReady. The timeseries
// path is driven by ViewDataReady through the manager runtime.
func StartEngineSubject(context.Context, *EngineApplicationConfig, *store.Store, trigger.CombinationTaskRunner, *taskrunner.OperationGate) (*eventconsumer.Consumer, error) {
	return nil, nil
}
