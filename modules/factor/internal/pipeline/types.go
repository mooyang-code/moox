package pipeline

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

type Mode int

const (
	ModeLive Mode = iota + 1
	ModeRecalc
)

type Plan struct {
	Mode           Mode
	Set            domain.FactorSet
	Factors        []domain.FactorDef
	TargetStart    time.Time
	TargetEnd      time.Time
	Expected       []string
	Available      []string
	UpstreamFailed []string
	CarryColumns   []string
	WriteCarry     bool
	TriggerEventID string
	Budget         time.Duration
}

type Outcome struct {
	Status         string
	FailedSubjects []string
	Factors        []storageio.FactorState
	RowsWritten    int
	StageDurations map[string]time.Duration
}

type Config struct {
	ReadBatchSubjects int
	ReadWorkers       int
	ReadRetries       int
	ReadTimeout       time.Duration
	PythonWorkers     int
	FactorsDir        string
}

type Runner struct {
	store storageio.Store
	exec  pyexec.Executor
	clock periodclock.Clock
	cfg   Config
}

func NewRunner(store storageio.Store, exec pyexec.Executor, clock periodclock.Clock, cfg Config) *Runner {
	if cfg.ReadBatchSubjects <= 0 {
		cfg.ReadBatchSubjects = 100
	}
	if cfg.ReadWorkers <= 0 {
		cfg.ReadWorkers = 4
	}
	if cfg.ReadRetries <= 0 {
		cfg.ReadRetries = 2
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 20 * time.Second
	}
	if cfg.PythonWorkers <= 0 {
		cfg.PythonWorkers = 1
	}
	return &Runner{store: store, exec: exec, clock: clock, cfg: cfg}
}

type LoadResult struct {
	Frames         map[string]*storageio.Frame
	Available      []string
	FailedSubjects []string
}

type Computation struct {
	Results      map[string]map[string]pyexec.ItemResult
	FactorStates map[string]storageio.FactorState
	BudgetHit    bool
}
