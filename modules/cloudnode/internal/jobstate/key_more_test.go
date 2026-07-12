package jobstate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseJobKeyRejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{
		"",
		"job.only-two-parts",
		"task.Y3J5cHRv.aXRlbQ",
		"job.@@@.aXRlbQ",
		"job.Y3J5cHRv.@@@",
	} {
		spaceID, jobItemID, ok := ParseJobKey(key)
		assert.False(t, ok, key)
		assert.Empty(t, spaceID)
		assert.Empty(t, jobItemID)
	}
}

func TestJobKeyTrimsSegments(t *testing.T) {
	key := JobKey(" crypto ", " item-1 ")
	spaceID, jobItemID, ok := ParseJobKey(key)
	assert.True(t, ok)
	assert.Equal(t, "crypto", spaceID)
	assert.Equal(t, "item-1", jobItemID)
}
