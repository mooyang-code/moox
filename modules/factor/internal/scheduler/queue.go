package scheduler

import "hash/fnv"

// HashSubject maps one subject to a stable worker shard.
func HashSubject(subjectID string, workers int) int {
	if workers <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(subjectID))
	return int(h.Sum32() % uint32(workers))
}

type queueItem struct {
	task Task
}
