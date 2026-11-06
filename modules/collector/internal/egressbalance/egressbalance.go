// Package egressbalance 让一批云函数的公网出口 IP 尽量分开。
//
// SCF 函数的出口来自云上共享的地址池，不同函数可能用同一个 IP；同一个 IP 上的函数合起来受数据源的频控。
// 在新建一批函数之后，探测每个函数的出口 IP，把共用同一个 IP 的函数交给调用方删掉重建（新实例会重新分到地址），
// 重复几轮，直到 IP 都分开、轮数用完，或地址池里的 IP 不够（连续两轮不同 IP 数没有增加）。
// 这个包不依赖任何市场或云厂商：探测由 Prober 完成，删除重建由调用方完成，什么时候可以重建由配置里的空闲时段决定。
package egressbalance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// Prober 探测一个函数此刻的公网出口 IP。
type Prober interface {
	ProbeEgressIP(ctx context.Context, spaceID, instanceID string) (string, error)
}

// Config 是去重的参数。
type Config struct {
	// MaxPerIP 是同一个出口 IP 上最多保留的函数数，默认 1。
	MaxPerIP int
	// Rounds 是最多做几轮“探测、重建”，默认 5。
	Rounds int
	// IdleWindow 是允许删除重建的时段（重建期间这些函数没有数据）；零值表示任何时候都可以。
	IdleWindow Window
}

// probeTimeout 是探测一个函数出口 IP 的超时：函数只是访问一次公网 IP 回显服务。
const probeTimeout = 20 * time.Second

// probeConcurrency 是同时探测的函数数。
const probeConcurrency = 5

// Balancer 记录一次去重的进度（只在进程内）。零值不可用，用 New 创建。
type Balancer struct {
	cfg                  Config
	pending              bool
	rounds, best, stalls int
}

// New 校验参数并创建 Balancer；参数为零的字段取默认值。
func New(cfg Config) (*Balancer, error) {
	if cfg.MaxPerIP <= 0 {
		cfg.MaxPerIP = 1
	}
	if cfg.Rounds <= 0 {
		cfg.Rounds = 5
	}
	if err := cfg.IdleWindow.validate(); err != nil {
		return nil, err
	}
	return &Balancer{cfg: cfg}, nil
}

// Arm 标记刚新建了一批函数，要做一次去重；进度从头开始。
func (b *Balancer) Arm() {
	if b == nil {
		return
	}
	b.pending, b.rounds, b.best, b.stalls = true, 0, 0, 0
}

// Pending 表示还有一次去重没做完。
func (b *Balancer) Pending() bool { return b != nil && b.pending }

// Next 做一轮：不在空闲时段、没有待做的去重时什么都不做；否则探测 names 里每个函数的出口 IP，返回应删掉重建的函数名。
// 有函数探测失败时这一轮不做决定（返回空，也不算一轮）。返回空且 Pending 变为 false，表示这次去重结束。
func (b *Balancer) Next(ctx context.Context, prober Prober, spaceID string, names []string, now time.Time) []string {
	if b == nil || !b.pending || prober == nil || !b.cfg.IdleWindow.Contains(now) {
		return nil
	}
	ips, failed := probeAll(ctx, prober, spaceID, names)
	if failed > 0 {
		log.WarnContextf(ctx, "egress balance probe incomplete space=%s failed=%d; decide next round", spaceID, failed)
		return nil
	}
	distinct := map[string]struct{}{}
	for _, ip := range ips {
		distinct[ip] = struct{}{}
	}
	duplicates := Duplicates(ips, b.cfg.MaxPerIP)
	if len(duplicates) == 0 {
		b.pending = false
		log.InfoContextf(ctx, "egress balance done: ips are spread space=%s functions=%d distinct_ips=%d", spaceID, len(ips), len(distinct))
		return nil
	}
	b.rounds++
	if len(distinct) > b.best {
		b.best, b.stalls = len(distinct), 0
	} else {
		b.stalls++
	}
	if b.rounds > b.cfg.Rounds || b.stalls >= 2 {
		b.pending = false
		log.WarnContextf(ctx, "egress balance stopped: ips stay shared, the address pool has too few ips space=%s functions=%d distinct_ips=%d rounds=%d", spaceID, len(ips), len(distinct), b.rounds-1)
		return nil
	}
	log.WarnContextf(ctx, "egress balance: recreate functions that share egress ips space=%s functions=%d distinct_ips=%d recreate=%s round=%d", spaceID, len(ips), len(distinct), strings.Join(duplicates, ","), b.rounds)
	return duplicates
}

func probeAll(ctx context.Context, prober Prober, spaceID string, names []string) (map[string]string, int) {
	var mu sync.Mutex
	ips := make(map[string]string, len(names))
	failed := 0
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			ip, err := prober.ProbeEgressIP(probeCtx, spaceID, name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || strings.TrimSpace(ip) == "" {
				failed++
				return
			}
			ips[name] = strings.TrimSpace(ip)
		}(name)
	}
	wg.Wait()
	return ips, failed
}

// Duplicates 返回需要重建的函数：同一个 IP 上超过 maxPerIP 个时，按函数名排序保留前 maxPerIP 个，其余返回。
func Duplicates(ips map[string]string, maxPerIP int) []string {
	if maxPerIP <= 0 {
		maxPerIP = 1
	}
	byIP := map[string][]string{}
	for name, ip := range ips {
		byIP[ip] = append(byIP[ip], name)
	}
	var out []string
	for _, names := range byIP {
		if len(names) <= maxPerIP {
			continue
		}
		sort.Strings(names)
		out = append(out, names[maxPerIP:]...)
	}
	sort.Strings(out)
	return out
}

// Window 是一天里的一个时段，按指定时区的时钟计算，起点晚于终点表示跨过午夜。零值表示全天。
type Window struct {
	// Start、End 是 "15:04" 格式的时刻，Start 含、End 不含；都为空表示全天。
	Start, End string
	// Timezone 是 IANA 时区名，默认 UTC。
	Timezone string
}

func (w Window) bounds() (start, end int, location *time.Location, err error) {
	if w.Start == "" && w.End == "" {
		return 0, 0, nil, nil
	}
	parse := func(value string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(value))
		if err != nil {
			return 0, fmt.Errorf("idle window time %q must look like 15:04", value)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	if start, err = parse(w.Start); err != nil {
		return 0, 0, nil, err
	}
	if end, err = parse(w.End); err != nil {
		return 0, 0, nil, err
	}
	location = time.UTC
	if w.Timezone != "" {
		if location, err = time.LoadLocation(w.Timezone); err != nil {
			return 0, 0, nil, fmt.Errorf("idle window timezone: %w", err)
		}
	}
	return start, end, location, nil
}

func (w Window) validate() error {
	if (w.Start == "") != (w.End == "") {
		return fmt.Errorf("idle window needs both start and end")
	}
	_, _, _, err := w.bounds()
	return err
}

// Contains 判断 t 是否落在时段内；零值时段包含任何时刻，配置有误时保守地返回 false。
func (w Window) Contains(t time.Time) bool {
	start, end, location, err := w.bounds()
	if err != nil {
		return false
	}
	if location == nil {
		return true
	}
	local := t.In(location)
	minutes := local.Hour()*60 + local.Minute()
	if start <= end {
		return minutes >= start && minutes < end
	}
	return minutes >= start || minutes < end
}
