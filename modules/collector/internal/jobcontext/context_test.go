package jobcontext

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobItemIDIsIsolatedAcrossSharedParentContext(t *testing.T) {
	parent := context.Background()
	const count = 100
	var wait sync.WaitGroup
	wait.Add(count)
	for index := 0; index < count; index++ {
		index := index
		go func() {
			defer wait.Done()
			jobItemID := fmt.Sprintf("job-%d", index)
			assert.Equal(t, jobItemID, JobItemID(WithJobItemID(parent, jobItemID)))
		}()
	}
	wait.Wait()
	assert.Empty(t, JobItemID(parent))
}
