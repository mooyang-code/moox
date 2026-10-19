package gatewayclient

import (
	"context"
	"net"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// warmKey 标识一个需要保持连接的目标：子池（TLS 身份）加网络与地址。
type warmKey struct {
	trust            trustIdentity
	network, address string
}

// warmTarget 记录预热某个地址所需的取连接参数，以及最近一次被请求使用的时间。
type warmTarget struct {
	opts     connpool.GetOptions
	lastUsed time.Time
}

// watchLocked 登记（或刷新）一个被请求使用的地址，并在第一次登记时启动预热协程；调用方持有 p.mu。
func (p *rpcPool) watchLocked(trust trustIdentity, network, address string, opts connpool.GetOptions) {
	key := warmKey{trust: trust, network: network, address: address}
	target := p.targets[key]
	if target == nil {
		opts.Ctx = nil
		target = &warmTarget{opts: opts}
		p.targets[key] = target
	}
	target.lastUsed = time.Now()
	p.warmStart.Do(func() {
		p.warmWG.Add(1)
		go p.warmLoop()
	})
}

// inBurstWindow 判断是否处在整分钟附近的业务突发窗口：采集函数每分钟第 0 秒触发，预热此时占用连接只会让请求多拨号。
func inBurstWindow(now time.Time) bool {
	second := now.Second()
	return second < burstWindowAfter || second >= 58
}

func (p *rpcPool) warmLoop() {
	defer p.warmWG.Done()
	ticker := time.NewTicker(warmInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.warmStop:
			return
		case now := <-ticker.C:
			if inBurstWindow(now) {
				continue
			}
			p.warmCycle(now)
		}
	}
}

// warmCycle 对每个最近被使用的地址检查打开连接数，不足 min_idle 时补齐。
func (p *rpcPool) warmCycle(now time.Time) {
	type job struct {
		key    warmKey
		target warmTarget
		open   int
	}
	var jobs []job
	p.mu.Lock()
	for key, target := range p.targets {
		if now.Sub(target.lastUsed) > warmWatchLimit {
			delete(p.targets, key)
			continue
		}
		if open := p.open[key.address]; open < p.settings.minIdle {
			jobs = append(jobs, job{key: key, target: *target, open: open})
		}
	}
	p.mu.Unlock()
	for _, item := range jobs {
		p.fill(item.key, item.target.opts, p.settings.minIdle)
	}
}

// fill 并发取 want 条连接再全部归还：池里已有的空闲连接被取走又放回，缺的部分现建，归还后都进入空闲列表。
func (p *rpcPool) fill(key warmKey, opts connpool.GetOptions, want int) {
	ctx, cancel := context.WithTimeout(context.Background(), warmDialTimeout)
	defer cancel()
	opts.WithDialTimeout(warmDialTimeout)
	conns := make([]net.Conn, want)
	var wg sync.WaitGroup
	for i := 0; i < want; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := opts
			o.WithContext(ctx)
			conn, err := p.Get(key.network, key.address, o)
			if err != nil {
				p.stats.warmupErr.Add(1)
				return
			}
			p.stats.warmupOK.Add(1)
			conns[i] = conn
		}(i)
	}
	wg.Wait()
	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}
