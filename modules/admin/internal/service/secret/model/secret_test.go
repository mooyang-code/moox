package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecret_TableName_ShouldReturnSecretsTable(t *testing.T) {
	assert.Equal(t, "t_secrets", (&Secret{}).TableName())
}
