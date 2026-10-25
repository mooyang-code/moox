package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"trpc.group/trpc-go/trpc-go/log"
)

// 窗口预载：引擎成为活动引擎（启动、接管租约）或者目录里出现新的因子集之后，立刻为集合准备数据：
// 读数据集字段、取同步基线或跟进变更、把本地补到最新已收盘的一根、载入内存窗口（设计 5.1）。
// 载入完成之前该集合的实时周期按预热处理：本期不算、记入账本，载入完成后由跳过恢复补算（见 gapfill.go）。

const (
	preloadInterval = time.Minute
	preloadTimeout  = 30 * time.Minute
)

type preloadCatalog interface {
	SetIDs() []string
	SetByID(setID string) (domain.EngineSet, SetLookup)
}

// preloadData 是预载用到的数据层能力。
type preloadData interface {
	Prepare(ctx context.Context, set domain.EngineSet, subjects []string) error
	// ReleaseSet 取消集合在数据源上的登记（集合离开目录或换了数据源）。
	ReleaseSet(set domain.FactorSet)
}

type preloader struct {
	sets   preloadCatalog
	source SourceDataset
	data   preloadData
	active func() bool
	// blocked 非空且返回 true 时不预载：模块暂停、恢复追赶、重置期间预载循环不能自己把数据拉回本地
	//（恢复追赶由生命周期的追赶钩子显式刷新，重置期间更不能把刚清掉的数据重新写进去）。
	blocked func() bool
	// onReady 在一个集合预载成功后调用（唤醒跳过恢复补算预载期间跳过的周期）。
	onReady func(setID string)
	kick    chan struct{}

	// prepared 记录每个集合预载成功时的状态：目录里的因子变了回看更长、集合的标的列表变了，都要重新预载（设计 6.2 移除标的）。
	mu       sync.Mutex
	prepared map[string]preparedState
}

// preparedState 是集合预载成功时的回看根数和标的范围的指纹。
type preparedState struct {
	// set 是预载时的集合：离开目录或换了数据源后，用它找到并取消旧数据源上的登记。
	set      domain.FactorSet
	lookback int
	scope    string
	// subjects 是预载时标的清单的指纹：「全部标的」的集合标的来自数据集，用户在数据集的标签里删除（或新增）标的后清单会变，
	// 变了要重新预载，窗口才会释放被删除的标的（设计 6.2）。
	subjects string
}

func subjectsSig(subjects []string) string {
	sorted := append([]string(nil), subjects...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// scopeOf 是集合预载范围的指纹：数据源（空间、源数据集、频率）。私有因子集改绑 View 后集合 ID 不变、数据源变了，
// 必须重新预载；标的清单来自数据集，由 subjectsChanged 检测变化。
func scopeOf(set domain.EngineSet) string {
	return set.Set.SpaceID + "/" + set.Set.SourceDatasetID + "/" + set.Set.Freq
}

// Ready 报告集合是否已经预载（窗口就绪）：生命周期检查用。
func (p *preloader) Ready(setID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.prepared[setID]
	return ok
}

// upToDate 报告集合是否已经按当前的回看和标的范围预载好。
func (p *preloader) upToDate(set domain.EngineSet) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.prepared[set.Set.SetID]
	return ok && state.lookback >= maxLookbackOf(set) && state.scope == scopeOf(set)
}

// releaseGone 取消已经离开目录的集合的登记（删除、停用）：数据源没有别的集合用时，窗口与同步随之释放。
func (p *preloader) releaseGone() {
	inCatalog := map[string]struct{}{}
	for _, id := range p.sets.SetIDs() {
		inCatalog[id] = struct{}{}
	}
	p.mu.Lock()
	var gone []preparedState
	for id, state := range p.prepared {
		if _, ok := inCatalog[id]; !ok {
			gone = append(gone, state)
			delete(p.prepared, id)
		}
	}
	p.mu.Unlock()
	for _, state := range gone {
		p.data.ReleaseSet(state.set)
		log.Infof("factor_preload_released set_id=%s source=%s/%s/%s reason=离开目录", state.set.SetID, state.set.SpaceID, state.set.SourceDatasetID, state.set.Freq)
	}
}

// releaseIfRebound 在集合换了数据源（私有因子集随实例改绑 View，集合 ID 不变）之后、重新预载之前，取消旧数据源上的登记。
func (p *preloader) releaseIfRebound(set domain.EngineSet) {
	p.mu.Lock()
	state, ok := p.prepared[set.Set.SetID]
	p.mu.Unlock()
	if !ok {
		return
	}
	old := state.set
	if old.SpaceID == set.Set.SpaceID && old.SourceDatasetID == set.Set.SourceDatasetID && old.Freq == set.Set.Freq {
		return
	}
	p.data.ReleaseSet(old)
	log.Infof("factor_preload_released set_id=%s source=%s/%s/%s reason=改绑数据源", old.SetID, old.SpaceID, old.SourceDatasetID, old.Freq)
}

// unmark 在重新预载之前清掉集合的成功记录：重新预载失败时集合不能还当作已就绪（旧的标的列表缓存会让后续重试被跳过）。
func (p *preloader) unmark(setID string) {
	p.mu.Lock()
	delete(p.prepared, setID)
	p.mu.Unlock()
}

func (p *preloader) markPrepared(set domain.EngineSet, subjects []string) {
	p.mu.Lock()
	p.prepared[set.Set.SetID] = preparedState{set: set.Set, lookback: maxLookbackOf(set), scope: scopeOf(set), subjects: subjectsSig(subjects)}
	p.mu.Unlock()
}

// subjectsChanged 报告集合的标的清单（数据集里状态为 active 的标的）自上次预载以来是否变了。
// 列清单失败时按没变处理，下一轮再查，不因一次失败去重新预载。
func (p *preloader) subjectsChanged(ctx context.Context, set domain.EngineSet) bool {
	if p.source == nil {
		return false
	}
	subjects, err := setSubjects(ctx, p.source, set, nil)
	if err != nil {
		return false
	}
	p.mu.Lock()
	state, ok := p.prepared[set.Set.SetID]
	p.mu.Unlock()
	return ok && state.subjects != "" && state.subjects != subjectsSig(subjects)
}

// PrepareSet 立刻为一个集合预载（生命周期的 init 动作）；已经预载且回看够长时直接返回。
func (p *preloader) PrepareSet(ctx context.Context, setID string) error {
	set, lookup := p.sets.SetByID(setID)
	if lookup != SetFound {
		return fmt.Errorf("因子集 %s 不在目录里", setID)
	}
	if p.upToDate(set) && !p.subjectsChanged(ctx, set) {
		return nil
	}
	p.unmark(setID)
	subjects, err := p.prepare(ctx, set)
	if err != nil {
		return err
	}
	p.markPrepared(set, subjects)
	if p.onReady != nil {
		p.onReady(setID)
	}
	return nil
}

// Refresh 重新同步一个集合的本地数据并重载窗口，不看「已经预载」的短路：恢复追赶要把暂停期间的缺口补进来。
func (p *preloader) Refresh(ctx context.Context, setID string) error {
	set, lookup := p.sets.SetByID(setID)
	if lookup != SetFound {
		return fmt.Errorf("因子集 %s 不在目录里", setID)
	}
	subjects, err := p.prepare(ctx, set)
	if err != nil {
		return err
	}
	p.markPrepared(set, subjects)
	if p.onReady != nil {
		p.onReady(setID)
	}
	return nil
}

func newPreloader(sets preloadCatalog, source SourceDataset, data preloadData, active func() bool, onReady func(string)) *preloader {
	return &preloader{sets: sets, source: source, data: data, active: active, onReady: onReady, kick: make(chan struct{}, 1), prepared: map[string]preparedState{}}
}

// Forget 忘掉所有集合的预载记录（重置清空本地数据之后，每个集合要重新预载）。
func (p *preloader) Forget() {
	p.mu.Lock()
	p.prepared = map[string]preparedState{}
	p.mu.Unlock()
}

// Kick 让预载循环尽快检查一次，不阻塞。引擎成为活动引擎、目录变化时调用。
func (p *preloader) Kick() {
	if p == nil {
		return
	}
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run 启动时、每次 Kick 以及每隔 preloadInterval 检查一次，直到 ctx 结束；预载失败的集合下次再试。
func (p *preloader) Run(ctx context.Context) {
	ticker := time.NewTicker(preloadInterval)
	defer ticker.Stop()
	for {
		p.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		case <-ticker.C:
		}
	}
}

func maxLookbackOf(set domain.EngineSet) int {
	n := 1
	for _, member := range set.Members {
		n = max(n, member.Factor.LookbackPeriods)
	}
	return n
}

// once 为目录里还没预载（或回看变长了）的因子集预载数据。已经预载好的集合每轮只是一次内存比对。
func (p *preloader) once(ctx context.Context) {
	if p.active != nil && !p.active() {
		return
	}
	if p.blocked != nil && p.blocked() {
		return
	}
	p.releaseGone()
	for _, setID := range p.sets.SetIDs() {
		if ctx.Err() != nil || (p.active != nil && !p.active()) {
			return
		}
		set, lookup := p.sets.SetByID(setID)
		if lookup != SetFound {
			continue
		}
		if p.upToDate(set) && !p.subjectsChanged(ctx, set) {
			continue
		}
		p.releaseIfRebound(set)
		p.unmark(setID)
		subjects, err := p.prepare(ctx, set)
		if err != nil && ctx.Err() == nil {
			if errors.Is(err, localdata.ErrChangeGap) {
				log.ErrorContextf(ctx, "factor_preload_blocked set_id=%s error=%v（请执行 moox-factor-engine reconcile 对账）", setID, err)
			} else {
				log.WarnContextf(ctx, "factor_preload_failed set_id=%s error=%v", setID, err)
			}
			continue
		}
		if err != nil {
			continue
		}
		p.markPrepared(set, subjects)
		if p.onReady != nil {
			p.onReady(setID)
		}
	}
}

func (p *preloader) prepare(ctx context.Context, set domain.EngineSet) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, preloadTimeout)
	defer cancel()
	started := time.Now()
	subjects, err := setSubjects(ctx, p.source, set, nil)
	if err != nil {
		return nil, err
	}
	if err := p.data.Prepare(ctx, set, subjects); err != nil {
		return nil, fmt.Errorf("预载集合 %s：%w", set.Set.SetID, err)
	}
	log.InfoContextf(ctx, "factor_preload_done set_id=%s subjects=%d lookback=%d duration=%s", set.Set.SetID, len(subjects), maxLookbackOf(set), time.Since(started).Round(time.Millisecond))
	return subjects, nil
}
