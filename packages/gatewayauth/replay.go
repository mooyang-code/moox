package gatewayauth

import (
	"errors"
	"sync"
	"time"
)

// Replay 是进程内的防重放记录。记录不落盘：进程重启后之前见过的 nonce 就丢了，
// 所以签名时间早于本进程启动时间的请求一律拒绝（时钟偏慢的调用方在网关重启后的头几十秒内会被拒，重签仍被拒就报错）。
type Replay struct {
	started time.Time
	mu      sync.Mutex
	seen    map[string]time.Time
	swept   time.Time
}

// NewReplay 以 started 作为本进程的启动时间。
func NewReplay(started time.Time) *Replay {
	return &Replay{started: started, seen: map[string]time.Time{}, swept: started}
}

// Accept 检查一次已验签的调用：签名时间不早于进程启动，nonce 在有效期内没见过。
func (r *Replay) Accept(claims Claims, now time.Time) error {
	if claims.Nonce == "" || claims.TTL <= 0 {
		return errors.New("防重放需要 nonce 与有效期")
	}
	if time.Unix(claims.Timestamp, 0).Before(r.started) {
		return errors.New("签名时间早于本进程启动")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 过期的记录每秒最多清一次，避免每个请求都扫一遍。
	if now.Sub(r.swept) >= time.Second {
		for nonce, expires := range r.seen {
			if !now.Before(expires) {
				delete(r.seen, nonce)
			}
		}
		r.swept = now
	}
	// nonce 是 32 字节随机数，不同调用方之间不会相同，不必再按调用方分开记。
	if expires, replayed := r.seen[claims.Nonce]; replayed && now.Before(expires) {
		return errors.New("请求重放")
	}
	r.seen[claims.Nonce] = now.Add(claims.TTL)
	return nil
}
