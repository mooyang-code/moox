package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultMaxMissingRatio = 0.1
	defaultMaxPeriods      = 30
)

// PeriodReader 读回源数据集某一期的行（判断没有事件可用的周期哪些标的已经到了）。
type PeriodReader interface {
	ReadWindow(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error)
}

// SetLookup 按集合 ID 找到它的源数据集（追赶时没有事件）。
type SetLookup interface {
	EnabledSetByDataset(context.Context, string, string, string) (domain.FactorSet, []domain.FactorDef, bool, error)
}

// ContinuityConfig 是连续水位的参数。
type ContinuityConfig struct {
	Store  *watermark.Store
	Reader PeriodReader
	// MaxMissingRatio 是缺失阈值：未确认缺失的失败标的 ÷ 期望标的超过它的周期视为无效，阻塞水位（默认 10%）；
	// SetRatios 按集合 ID 覆盖。
	MaxMissingRatio float64
	SetRatios       map[string]float64
	// MaxPeriods 是一次推进最多处理的周期数，剩下的交给追赶循环。
	MaxPeriods int
	Now        func() time.Time
}

// Continuity 维护每个集合的连续水位：从水位下一期逐期处理到最新已知周期，前面有还没补到的无效周期就停住，
// 水位不前进；当期缺失在阈值以内时先算已到的标的，缺的记进空洞账本由补采后的只填充补算补上。
type Continuity struct {
	cfg ContinuityConfig

	mu sync.Mutex
	// absent 是补采确认行情源没有的 (集合, 周期) 标的：不计入缺失，也不阻塞。
	absent map[string]map[int64]absentEntry
}

type absentEntry struct {
	subjects map[string]struct{}
	until    time.Time
}

// NewContinuity 构造连续水位推进。
func NewContinuity(cfg ContinuityConfig) (*Continuity, error) {
	if cfg.Store == nil || cfg.Reader == nil {
		return nil, errors.New("continuity requires a watermark store and a period reader")
	}
	if cfg.MaxMissingRatio <= 0 {
		cfg.MaxMissingRatio = defaultMaxMissingRatio
	}
	if cfg.MaxPeriods <= 0 {
		cfg.MaxPeriods = defaultMaxPeriods
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Continuity{cfg: cfg, absent: map[string]map[int64]absentEntry{}}, nil
}

// States 返回全部集合的推进状态（心跳上报与指标用）。
func (c *Continuity) States() []watermark.State { return c.cfg.Store.All() }

// NoteAbsent 记下补采确认行情源没有的标的（补齐通知带来），30 分钟后过期。
func (c *Continuity) NoteAbsent(setID string, from, to time.Time, step time.Duration, subjects []string) {
	if len(subjects) == 0 || step <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.absent[setID] == nil {
		c.absent[setID] = map[int64]absentEntry{}
	}
	until := c.cfg.Now().Add(30 * time.Minute)
	for period := from.UTC(); period.Before(to); period = period.Add(step) {
		entry := c.absent[setID][period.Unix()]
		if entry.subjects == nil {
			entry.subjects = map[string]struct{}{}
		}
		for _, subject := range subjects {
			entry.subjects[strings.TrimSpace(subject)] = struct{}{}
		}
		entry.until = until
		c.absent[setID][period.Unix()] = entry
	}
}

func (c *Continuity) absentAt(setID string, period time.Time) map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	for key, entry := range c.absent[setID] {
		if now.After(entry.until) {
			delete(c.absent[setID], key)
		}
	}
	return c.absent[setID][period.Unix()].subjects
}

func (c *Continuity) ratioFor(setID string) float64 {
	if ratio, ok := c.cfg.SetRatios[setID]; ok && ratio > 0 {
		return ratio
	}
	return c.cfg.MaxMissingRatio
}

// onEvent 记下事件带来的最新周期与期望标的，然后推进。
func (c *Continuity) onEvent(ctx context.Context, h *Handler, set domain.FactorSet, factors []domain.FactorDef, event periodEvent) error {
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return err
	}
	step, err := clock.Duration(set.Freq)
	if err != nil {
		return err
	}
	period, err := clock.Align(event.periodTime, set.Freq)
	if err != nil {
		return err
	}
	state, ok := c.cfg.Store.Get(set.SetID)
	if !ok || state.CommittedThrough.IsZero() {
		// 第一次见到这个集合：从这一期起保证连续，不回头补历史（历史用补算任务）。
		state = watermark.State{SetID: set.SetID, CommittedThrough: period.Add(-step)}
	}
	state.SpaceID, state.DatasetID, state.Freq = set.SpaceID, set.SourceDatasetID, set.Freq
	if period.After(state.Target) {
		state.Target = period
	}
	if len(event.universe) > 0 && !period.Before(state.Target) {
		state.Universe = append([]string(nil), event.universe...)
	}
	if err := c.cfg.Store.Put(state); err != nil {
		return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
	}
	return c.advance(ctx, h, set, factors, step, &event)
}

// advanceSet 在没有事件时推进（追赶循环、补齐通知）。
func (c *Continuity) advanceSet(ctx context.Context, h *Handler, setID string) error {
	state, ok := c.cfg.Store.Get(setID)
	if !ok || !state.Behind() {
		return nil
	}
	set, factors, found, err := h.sets.EnabledSetByDataset(ctx, state.SpaceID, state.DatasetID, state.Freq)
	if err != nil || !found || set.Status != domain.SetStatusEnabled || set.SetID != setID {
		return err
	}
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return err
	}
	step, err := clock.Duration(set.Freq)
	if err != nil {
		return err
	}
	return c.advance(ctx, h, set, factors, step, nil)
}

// advance 从水位下一期逐期处理到最新已知周期（一次至多 MaxPeriods 期）。
func (c *Continuity) advance(ctx context.Context, h *Handler, set domain.FactorSet, factors []domain.FactorDef, step time.Duration, event *periodEvent) error {
	state, _ := c.cfg.Store.Get(set.SetID)
	for processed := 0; state.Behind() && processed < c.cfg.MaxPeriods; processed++ {
		period := state.CommittedThrough.Add(step)
		universe, failed, triggerID := state.Universe, []string(nil), CatchupEventID(set.SetID, period)
		if event != nil && event.periodTime.Equal(period) {
			universe, failed, triggerID = event.universe, event.failed, event.eventID
		} else {
			missing, err := c.readback(ctx, h, set, factors, period, universe)
			if err != nil {
				return err
			}
			failed = missing
		}
		expected, unconfirmed := c.missing(set, period, universe, failed)
		if reason := c.invalid(set.SetID, expected, unconfirmed); reason != "" {
			if state.BlockedSince.IsZero() || !state.BlockedPeriod.Equal(period) {
				if state.BlockedSince.IsZero() {
					state.BlockedSince = c.cfg.Now().UTC()
				}
				log.WarnContextf(ctx, "factor_watermark_blocked set_id=%s period=%s reason=%s missing=%d expected=%d", set.SetID, period.Format(time.RFC3339), reason, len(unconfirmed), len(expected))
			}
			state.BlockedPeriod, state.BlockedReason = period, reason
			if err := c.cfg.Store.Put(state); err != nil {
				return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
			}
			return nil
		}
		if _, err := h.computePeriod(ctx, set, factors, period, universe, unconfirmed, triggerID); err != nil {
			return err
		}
		if !state.BlockedSince.IsZero() {
			log.InfoContextf(ctx, "factor_watermark_unblocked set_id=%s period=%s blocked_for=%s", set.SetID, period.Format(time.RFC3339), c.cfg.Now().Sub(state.BlockedSince).Truncate(time.Second))
		}
		state.CommittedThrough, state.BlockedSince, state.BlockedPeriod, state.BlockedReason = period, time.Time{}, time.Time{}, ""
		if err := c.cfg.Store.Put(state); err != nil {
			return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
		}
	}
	return nil
}

// missing 返回该期期望的标的（按集合范围）与未确认缺失的失败标的。
func (c *Continuity) missing(set domain.FactorSet, period time.Time, universe, failed []string) ([]string, []string) {
	absent := c.absentAt(set.SetID, period)
	var expected []string
	for _, subject := range uniqueStrings(universe) {
		if !set.InScope(subject) {
			continue
		}
		if _, confirmed := absent[subject]; confirmed {
			continue
		}
		expected = append(expected, subject)
	}
	inExpected := make(map[string]struct{}, len(expected))
	for _, subject := range expected {
		inExpected[subject] = struct{}{}
	}
	var unconfirmed []string
	for _, subject := range uniqueStrings(failed) {
		if _, ok := inExpected[subject]; ok {
			unconfirmed = append(unconfirmed, subject)
		}
	}
	return expected, unconfirmed
}

// invalid 判断一期是否无效：不知道期望标的、零提交（全部缺失）、缺失超过阈值。
func (c *Continuity) invalid(setID string, expected, missing []string) string {
	switch {
	case len(expected) == 0:
		return "no_universe"
	case len(missing) == len(expected):
		return "no_rows"
	case float64(len(missing))/float64(len(expected)) > c.ratioFor(setID):
		return "missing_over_threshold"
	}
	return ""
}

// readback 读回一期源数据，返回还没有行的期望标的。
func (c *Continuity) readback(ctx context.Context, h *Handler, set domain.FactorSet, factors []domain.FactorDef, period time.Time, universe []string) ([]string, error) {
	var subjects []string
	for _, subject := range uniqueStrings(universe) {
		if set.InScope(subject) {
			subjects = append(subjects, subject)
		}
	}
	if len(subjects) == 0 {
		return nil, nil
	}
	column := "close"
	for _, factor := range factors {
		if len(factor.InputColumns) > 0 {
			column = factor.InputColumns[0]
			break
		}
	}
	step := periodStep(set)
	readCtx, cancel := context.WithTimeout(ctx, h.cfg.ReadColumnTimeout)
	defer cancel()
	frames, err := c.cfg.Reader.ReadWindow(readCtx, storageio.ReadRequest{SpaceID: set.SpaceID, DatasetID: set.SourceDatasetID, Freq: set.Freq, Subjects: subjects, Start: period, End: period.Add(step), Columns: []string{column}})
	if err != nil {
		return nil, fmt.Errorf("%w: read back period %s: %v", storageio.ErrInfra, period.Format(time.RFC3339), err)
	}
	var missing []string
	for _, subject := range subjects {
		if frame := frames[subject]; frame == nil || len(frame.Rows) == 0 {
			missing = append(missing, subject)
		}
	}
	return missing, nil
}

func periodStep(set domain.FactorSet) time.Duration {
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return time.Minute
	}
	step, err := clock.Duration(set.Freq)
	if err != nil {
		return time.Minute
	}
	return step
}

// CatchupEventID 是没有事件可用、靠读回推进的周期使用的触发事件 ID。
func CatchupEventID(setID string, period time.Time) string {
	return fmt.Sprintf("catchup:%s:%d", setID, period.Unix())
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
