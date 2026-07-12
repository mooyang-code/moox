package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSvcDecimalOps_ValidInputs_ShouldCalculate(t *testing.T) {
	got, err := addSvc("", "1.5")
	require.NoError(t, err)
	assert.Equal(t, "1.5", got)

	got, err = subSvc("5", "2.25")
	require.NoError(t, err)
	assert.Equal(t, "2.75", got)

	got, err = mulSvc("1.5", "2")
	require.NoError(t, err)
	assert.Equal(t, "3", got)

	got, err = divSvcSafe("7.5", "2.5")
	require.NoError(t, err)
	assert.Equal(t, "3", got)
}

func TestSvcDecimalOps_InvalidInputs_ShouldReturnError(t *testing.T) {
	_, err := addSvc("bad", "1")
	assert.Error(t, err)

	_, err = addSvc("1", "bad")
	assert.Error(t, err)

	_, err = subSvc("bad", "1")
	assert.Error(t, err)

	_, err = subSvc("1", "bad")
	assert.Error(t, err)

	_, err = mulSvc("bad", "1")
	assert.Error(t, err)

	_, err = mulSvc("1", "bad")
	assert.Error(t, err)

	_, err = divSvcSafe("1", "bad")
	assert.Error(t, err)

	_, err = divSvcSafe("bad", "1")
	assert.Error(t, err)
}

func TestDivSvcSafe_ZeroDivisor_ShouldReturnZero(t *testing.T) {
	got, err := divSvcSafe("10", "0")
	require.NoError(t, err)
	assert.Equal(t, "0", got)
}
