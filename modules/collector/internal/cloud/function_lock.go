package cloud

import "sync"

// functionLocks 按函数（账号、地域、命名空间、函数名）串行写操作：同一个函数的 Ensure、UpgradeCode、SetTimer、Delete
// 排队等待，不并发改云上状态。锁只在本进程内，Collector 是唯一改云函数的进程。
var functionLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

// lockFunction 拿到一个函数的写锁，返回释放函数。
func lockFunction(ref FunctionRef) func() {
	key := ref.AccountID + "\x00" + ref.Region + "\x00" + ref.Namespace + "\x00" + ref.Name
	functionLocks.mu.Lock()
	lock := functionLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		functionLocks.locks[key] = lock
	}
	functionLocks.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
