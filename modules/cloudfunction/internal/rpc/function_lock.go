package rpc

import (
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
)

type functionLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var scfInstanceLocks = functionLockRegistry{locks: make(map[string]*sync.Mutex)}

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

func lockFunctionInstance(spaceID, instanceID string) func() {
	key := spaceID + "\x00" + instanceID
	scfInstanceLocks.mu.Lock()
	lock := scfInstanceLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		scfInstanceLocks.locks[key] = lock
	}
	scfInstanceLocks.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func lockFunctionInstances(spaceID string, instanceIDs []string) func() {
	ordered := orderedUniqueSCFInstanceIDs(instanceIDs)
	unlock := make([]func(), 0, len(ordered))
	for _, instanceID := range ordered {
		unlock = append(unlock, lockFunctionInstance(spaceID, instanceID))
	}
	return func() {
		for i := len(unlock) - 1; i >= 0; i-- {
			unlock[i]()
		}
	}
}

func orderedUniqueSCFInstanceIDs(instanceIDs []string) []string {
	ordered := append([]string(nil), instanceIDs...)
	sort.Strings(ordered)
	unique := ordered[:0]
	for _, instanceID := range ordered {
		if instanceID == "" || (len(unique) > 0 && unique[len(unique)-1] == instanceID) {
			continue
		}
		unique = append(unique, instanceID)
	}
	return unique
}
