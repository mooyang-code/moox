package marketfetch

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
)

// EgressBalance 是函数出口 IP 去重的参数。SCF 函数的公网出口来自云上的共享地址池，不同函数可能用同一个 IP，
// 同一个 IP 上的函数合起来受数据源的频控；探测出口后把共用 IP 的函数删掉重建（新实例会重新分到地址），尽量让每个 IP 上只有 MaxPerIP 个函数。
type EgressBalance struct {
	// MaxPerIP 是同一个出口 IP 上最多保留的函数数（默认 1）。
	MaxPerIP int
	// Rounds 是最多做几轮“探测、重建”；地址池里的 IP 不够多时，重建也换不到新 IP，到轮数或连续两轮没有改善就停止。
	Rounds int
}

// egressProbeTimeout 是探测一个函数出口 IP 的超时；探测只是调用函数访问一次公网 IP 回显服务。
const egressProbeTimeout = 20 * time.Second

// egressProber 由能调用函数的云端客户端实现：返回函数当前的公网出口 IP。
type egressProber interface {
	ProbeEgressIP(ctx context.Context, spaceID, instanceID string) (string, error)
}

// egressDuplicates 返回需要重建的函数：同一个 IP 上超过 maxPerIP 个时，按函数名排序保留前 maxPerIP 个，其余返回。
func egressDuplicates(ips map[string]string, maxPerIP int) []string {
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

// egressRecreate 在新建函数之后、核对云端函数没有其他问题时探测各函数的出口 IP，返回要删掉重建的函数名（交给原有的“云端缺失重建”流程）。
// 删除重建期间这些函数没有数据，所以只在 EgressIdle 允许的时段（例如收盘后）做；探测失败的函数让这一轮不做决定，等下一轮。
func (r *PlanReconciler) egressRecreate(ctx context.Context, names []string, byName map[string]scfinvoker.FunctionInstance) []string {
	balance := r.EgressBalance
	if balance == nil || !r.egressPending || r.EgressIdle == nil || !r.EgressIdle(r.now()) {
		return nil
	}
	prober, ok := r.Cloud.(egressProber)
	if !ok {
		return nil
	}
	var mu sync.Mutex
	ips := make(map[string]string, len(names))
	failed := 0
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, name := range names {
		if _, exists := byName[name]; !exists {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, egressProbeTimeout)
			defer cancel()
			ip, err := prober.ProbeEgressIP(probeCtx, r.SpaceID, name)
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
	if failed > 0 {
		log.WarnContextf(ctx, "collector fetch plan egress probe incomplete space=%s failed=%d; decide next round", r.SpaceID, failed)
		return nil
	}
	distinct := map[string]struct{}{}
	for _, ip := range ips {
		distinct[ip] = struct{}{}
	}
	duplicates := egressDuplicates(ips, balance.MaxPerIP)
	if len(duplicates) == 0 {
		r.egressPending = false
		log.InfoContextf(ctx, "collector fetch plan egress ips are spread space=%s functions=%d distinct_ips=%d", r.SpaceID, len(ips), len(distinct))
		return nil
	}
	r.egressRounds++
	if len(distinct) > r.egressBest {
		r.egressBest, r.egressStall = len(distinct), 0
	} else {
		r.egressStall++
	}
	if r.egressRounds > max(balance.Rounds, 1) || r.egressStall >= 2 {
		r.egressPending = false
		log.WarnContextf(ctx, "collector fetch plan egress ips stay shared space=%s functions=%d distinct_ips=%d rounds=%d: the address pool has too few ips, keep as is", r.SpaceID, len(ips), len(distinct), r.egressRounds-1)
		return nil
	}
	log.WarnContextf(ctx, "collector fetch plan recreating functions that share egress ips space=%s functions=%d distinct_ips=%d recreate=%s round=%d", r.SpaceID, len(ips), len(distinct), strings.Join(duplicates, ","), r.egressRounds)
	return duplicates
}

// StockCNIdleForRecreate 判断 A 股此刻是否空闲到可以删除重建函数：北京时间 15:10 之后到次日 09:00 之前。
// 重建一个 VPC 函数要几十秒到几分钟，期间它负责的标的没有数据，所以不在交易时段做。
func StockCNIdleForRecreate(now time.Time) bool {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return false
	}
	local := now.In(location)
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 15*60+10 || minutes < 9*60
}
