package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUser_TableName_ShouldReturnUsersTable(t *testing.T) {
	assert.Equal(t, "t_users", (&User{}).TableName())
}

func TestLoginHistory_TableName_ShouldReturnLoginHistoryTable(t *testing.T) {
	assert.Equal(t, "t_login_history", (&LoginHistory{}).TableName())
}
