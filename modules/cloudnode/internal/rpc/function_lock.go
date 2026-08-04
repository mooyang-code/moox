package rpc

import (
	"sync"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
)

type functionLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var scfFunctionLocks = functionLockRegistry{locks: make(map[string]*sync.Mutex)}

func lockSCFFunction(ref tencentscf.FunctionRef) func() {
	key := ref.Region + "\x00" + ref.Namespace + "\x00" + ref.FunctionName
	scfFunctionLocks.mu.Lock()
	lock := scfFunctionLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		scfFunctionLocks.locks[key] = lock
	}
	scfFunctionLocks.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
