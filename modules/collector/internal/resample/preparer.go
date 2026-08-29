package resample

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// Preparer asynchronously makes the target catalog ready. Metadata calls are
// intentionally outside the Collector SQLite transaction.
type Preparer struct {
	Rules        *store.TaskRuleRepository
	Source       subjectSource
	Catalog      *Catalog
	KeepDuration string
	Limit        int
	mu           sync.Mutex
}

func (p *Preparer) RunOnce(ctx context.Context) error {
	if p == nil || p.Rules == nil || p.Source == nil || p.Catalog == nil {
		return fmt.Errorf("resample preparer dependencies are required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	rules, err := p.Rules.ListResampleByPrepareStates(ctx, []domain.TaskRulePrepareState{
		domain.PrepareStatePending, domain.PrepareStateWaitingView, domain.PrepareStateError, domain.PrepareStateReady,
	}, limit)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		params, parseErr := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
		if parseErr != nil {
			_ = p.Rules.SetPrepareState(ctx, rule.SpaceID, rule.RuleID, domain.PrepareStateError, parseErr.Error())
			continue
		}
		source, sourceErr := p.Source.GetDataset(ctx, rule.SpaceID, params.SourceDatasetID)
		if sourceErr == nil {
			var subjects []domain.DatasetSubject
			if sourceWithRuleUniverse, ok := p.Source.(resampleRuleSubjectSource); ok {
				subjects, sourceErr = sourceWithRuleUniverse.ListResampleSubjectsForRule(ctx, rule.SpaceID, params.SourceDatasetID, rule.Provider, params.SourceSeriesTag)
			} else if sourceWithNativeUniverse, ok := p.Source.(resampleSubjectSource); ok {
				subjects, sourceErr = sourceWithNativeUniverse.ListResampleSubjects(ctx, rule.SpaceID, params.SourceDatasetID)
			} else {
				subjects, sourceErr = p.Source.ListSubjects(ctx, rule.SpaceID, params.SourceDatasetID, source.DataSourceID)
			}
			if sourceErr == nil {
				sourceErr = p.Catalog.PrepareTarget(ctx, rule, params, source, subjects, p.KeepDuration)
			}
		}
		if sourceErr != nil {
			state := domain.PrepareStateError
			if errors.Is(sourceErr, ErrTargetViewNotReady) {
				state = domain.PrepareStateWaitingView
			}
			_ = p.Rules.SetPrepareState(ctx, rule.SpaceID, rule.RuleID, state, sourceErr.Error())
			continue
		}
		if err := p.Rules.SetPrepareState(ctx, rule.SpaceID, rule.RuleID, domain.PrepareStateReady, ""); err != nil {
			return err
		}
	}
	return nil
}

// Retry marks an error rule as pending for an explicit operator retry.
func (p *Preparer) Retry(ctx context.Context, spaceID, ruleID string) error {
	if p == nil || p.Rules == nil {
		return fmt.Errorf("resample preparer is not initialized")
	}
	return p.Rules.SetPrepareState(ctx, strings.TrimSpace(spaceID), strings.TrimSpace(ruleID), domain.PrepareStatePending, "")
}

// Start launches the bounded preparation loop. A timer should still invoke
// RunOnce; the loop is a liveness fallback for rules created while no timer is
// configured in a development environment.
func (p *Preparer) Start(ctx context.Context, interval time.Duration) (func(), error) {
	if ctx == nil {
		return nil, fmt.Errorf("resample preparer context is required")
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_ = p.RunOnce(loopCtx)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

var _ subjectSource = (*storagesource.DatasetSource)(nil)
