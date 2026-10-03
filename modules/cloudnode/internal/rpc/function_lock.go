package rpc

import (
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
)

type functionLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var scfNodeLocks = functionLockRegistry{locks: make(map[string]*sync.Mutex)}

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

func lockSCFNode(spaceID, nodeID string) func() {
	key := spaceID + "\x00" + nodeID
	scfNodeLocks.mu.Lock()
	lock := scfNodeLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		scfNodeLocks.locks[key] = lock
	}
	scfNodeLocks.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func lockSCFNodes(spaceID string, nodeIDs []string) func() {
	ordered := orderedUniqueSCFNodeIDs(nodeIDs)
	unlock := make([]func(), 0, len(ordered))
	for _, nodeID := range ordered {
		unlock = append(unlock, lockSCFNode(spaceID, nodeID))
	}
	return func() {
		for i := len(unlock) - 1; i >= 0; i-- {
			unlock[i]()
		}
	}
}

func orderedUniqueSCFNodeIDs(nodeIDs []string) []string {
	ordered := append([]string(nil), nodeIDs...)
	sort.Strings(ordered)
	unique := ordered[:0]
	for _, nodeID := range ordered {
		if nodeID == "" || (len(unique) > 0 && unique[len(unique)-1] == nodeID) {
			continue
		}
		unique = append(unique, nodeID)
	}
	return unique
}
