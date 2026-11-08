package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// avoidanceRefreshEvery 是避让规则的刷新间隔（距上次成功）；没成功过或上次有空间失败时隔 avoidanceRetryAfter 重试。
	avoidanceRefreshEvery = 10 * time.Minute
	// avoidanceReadBudget 是一次刷新读取全部空间的总时长上限（各空间并行）。
	avoidanceReadBudget = 20 * time.Second
	// avoidanceRetryAfter 是读取失败后至少隔多久再读：Collector 不可用时不让每分钟的维护都卡满读取上限。
	avoidanceRetryAfter = 3 * time.Minute
)

// CollectorFrequencies 读取空间生效中的采集函数服务的频率（来自 CollectMgr.GetCollectorStatus）。
type CollectorFrequencies interface {
	SpaceFrequencies(ctx context.Context, spaceID string) ([]string, error)
}

// WithCollector 设置采集频率来源；不设置时避让规则一直是默认值。
func WithCollector(c CollectorFrequencies) Option { return func(s *Service) { s.collector = c } }

// avoidanceState 是内存里的避让规则（不落库，重启后从默认值开始）。
type avoidanceState struct {
	mu sync.RWMutex
	// current 是当前生效的规则快照，发布后不再修改；nil 表示还没算过，用默认值。
	current *schedule.Avoidance
	// spaces 是各空间最近一次成功读到的频率。
	spaces map[string]schedule.SpaceFrequencies
	// lastOK 是最近一次全部空间都读取成功的时间；lastSpaces 是那次计算用的空间（有归档任务的空间）。
	lastOK     time.Time
	lastSpaces string
	// refreshing 防止上一轮刷新未结束时重入。
	refreshing bool
	// lastFailed 是最近一次读取失败的时间；成功后清零。
	lastFailed time.Time
}

// avoidance 返回当前生效的避让规则快照（调用方不得修改）。
func (s *Service) avoidance() *schedule.Avoidance {
	s.avoid.mu.RLock()
	defer s.avoid.mu.RUnlock()
	if s.avoid.current == nil {
		d := schedule.DefaultAvoidance()
		return &d
	}
	return s.avoid.current
}

// maybeRefreshAvoidance 距上次成功满 10 分钟、或有归档任务的空间变了（如新建了第一个任务）时刷新避让规则；
// 上次读取失败时至少隔 3 分钟再读。
func (s *Service) maybeRefreshAvoidance(ctx context.Context, now time.Time) error {
	if s.collector == nil {
		return nil
	}
	spaces, err := s.taskSpaces(ctx)
	if err != nil {
		return err
	}
	key := strings.Join(spaces, ",")
	s.avoid.mu.Lock()
	due := !s.avoid.refreshing && (s.avoid.lastOK.IsZero() || key != s.avoid.lastSpaces || now.Sub(s.avoid.lastOK) >= avoidanceRefreshEvery) &&
		(s.avoid.lastFailed.IsZero() || now.Sub(s.avoid.lastFailed) >= avoidanceRetryAfter)
	if due {
		s.avoid.refreshing = true
	}
	s.avoid.mu.Unlock()
	if !due {
		return nil
	}
	defer func() {
		s.avoid.mu.Lock()
		s.avoid.refreshing = false
		s.avoid.mu.Unlock()
	}()
	return s.refreshAvoidance(ctx, spaces, now)
}

// taskSpaces 返回有归档任务的空间（排序、去重）。
func (s *Service) taskSpaces(ctx context.Context) ([]string, error) {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return nil, fmt.Errorf("列出归档任务: %w", err)
	}
	seen := map[string]bool{}
	var spaces []string
	for _, t := range tasks {
		if !seen[t.SpaceID] {
			seen[t.SpaceID] = true
			spaces = append(spaces, t.SpaceID)
		}
	}
	sort.Strings(spaces)
	return spaces, nil
}

// refreshAvoidance 并行读取给定空间的采集频率，重算避让规则。
// 读取失败的空间沿用上次成功的结果（Basis 注明）；任一空间失败时返回错误，3 分钟后重试。
func (s *Service) refreshAvoidance(ctx context.Context, spaces []string, now time.Time) error {
	seen := make(map[string]bool, len(spaces))
	for _, space := range spaces {
		seen[space] = true
	}
	readCtx, cancel := context.WithTimeout(ctx, avoidanceReadBudget)
	defer cancel()
	type result struct {
		freqs []string
		err   error
	}
	results := make([]result, len(spaces))
	var wg sync.WaitGroup
	for i, space := range spaces {
		wg.Add(1)
		go func(i int, space string) {
			defer wg.Done()
			freqs, err := s.collector.SpaceFrequencies(readCtx, space)
			results[i] = result{freqs: freqs, err: err}
		}(i, space)
	}
	wg.Wait()

	s.avoid.mu.Lock()
	defer s.avoid.mu.Unlock()
	if s.avoid.spaces == nil {
		s.avoid.spaces = map[string]schedule.SpaceFrequencies{}
	}
	inputs := make([]schedule.SpaceFrequencies, 0, len(spaces))
	var errs []error
	for i, space := range spaces {
		if err := results[i].err; err != nil {
			errs = append(errs, fmt.Errorf("读取空间 %s 的采集频率: %w", space, err))
			prev := s.avoid.spaces[space]
			inputs = append(inputs, schedule.SpaceFrequencies{SpaceID: space, Freqs: prev.Freqs, FetchedAt: prev.FetchedAt, Failed: true})
			continue
		}
		got := schedule.SpaceFrequencies{SpaceID: space, Freqs: results[i].freqs, FetchedAt: now}
		s.avoid.spaces[space] = got
		inputs = append(inputs, got)
	}
	// 已经没有任务的空间不再保留。
	for space := range s.avoid.spaces {
		if !seen[space] {
			delete(s.avoid.spaces, space)
		}
	}
	computed := schedule.ComputeAvoidance(inputs, now)
	if s.avoid.current == nil || !sameRules(*s.avoid.current, computed) {
		log.InfoContextf(ctx, "archive avoidance updated: every=%dm before=%ds after=%ds hour_quiet=%v basis=%s",
			computed.EveryMinutes, computed.BeforeSeconds, computed.AfterSeconds, computed.HourQuiet, computed.Basis)
	}
	s.avoid.current = &computed
	if len(errs) > 0 {
		s.avoid.lastFailed = now
		return errors.Join(errs...)
	}
	s.avoid.lastOK, s.avoid.lastSpaces, s.avoid.lastFailed = now, strings.Join(spaces, ","), time.Time{}
	return nil
}

func sameRules(a, b schedule.Avoidance) bool {
	if a.EveryMinutes != b.EveryMinutes || a.BeforeSeconds != b.BeforeSeconds || a.AfterSeconds != b.AfterSeconds {
		return false
	}
	if (a.HourQuiet == nil) != (b.HourQuiet == nil) {
		return false
	}
	return a.HourQuiet == nil || *a.HourQuiet == *b.HourQuiet
}

// avoidanceToPB 把避让规则转成接口消息。
func avoidanceToPB(a *schedule.Avoidance) *archivepb.AvoidanceRules {
	if a == nil {
		return nil
	}
	out := &archivepb.AvoidanceRules{
		EveryMinutes: uint32(a.EveryMinutes), BeforeSeconds: uint32(a.BeforeSeconds), AfterSeconds: uint32(a.AfterSeconds),
		Basis: a.Basis, ComputedAt: fmtRFC(a.ComputedAt),
	}
	if a.HourQuiet != nil {
		out.HourQuietFrom, out.HourQuietUntil = a.HourQuiet[0], a.HourQuiet[1]
	}
	return out
}
