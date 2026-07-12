package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHSession_TableName_ShouldReturnSSHSessionTable(t *testing.T) {
	assert.Equal(t, "t_ssh_session", (&SSHSession{}).TableName())
}

func TestSSHHost_TableName_ShouldReturnSSHHostTable(t *testing.T) {
	assert.Equal(t, "t_ssh_host", (&SSHHost{}).TableName())
}
