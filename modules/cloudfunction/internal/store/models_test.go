package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModelTableNames(t *testing.T) {
	assert.Equal(t, "t_cloud_function_instances", (&FunctionInstance{}).TableName())
	assert.Equal(t, "t_cloud_accounts", (&CloudAccount{}).TableName())
	assert.Equal(t, "t_cloud_function_packages", (&FunctionPackage{}).TableName())
	assert.Equal(t, "t_cloud_function_jobs", (&FunctionJob{}).TableName())
	assert.Equal(t, "t_cloud_function_job_items", (&FunctionJobItem{}).TableName())
}
