package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashSubject(t *testing.T) {
	assert.Equal(t, 0, HashSubject("BTC", 0))
	a := HashSubject("BTC-USDT", 4)
	b := HashSubject("BTC-USDT", 4)
	assert.Equal(t, a, b)
	assert.GreaterOrEqual(t, a, 0)
	assert.Less(t, a, 4)
}
