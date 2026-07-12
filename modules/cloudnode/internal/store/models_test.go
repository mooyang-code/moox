package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModelTableNames(t *testing.T) {
	assert.Equal(t, "t_cloud_nodes", (&CloudNode{}).TableName())
	assert.Equal(t, "t_cloud_accounts", (&CloudAccount{}).TableName())
	assert.Equal(t, "t_cloud_function_packages", (&FunctionPackage{}).TableName())
}
