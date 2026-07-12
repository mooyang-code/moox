package auth

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestAuthOperator_RegisterUser_NilConfig_ShouldReturnError(t *testing.T) {
	op := NewAuthOperator(nil)
	_, err := op.RegisterUser(context.Background(), "alice", "pwd", "nick", "a@b.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "认证服务配置不存在")
}

func TestAuthOperator_RegisterUser_EmptyAuthTarget_ShouldReturnError(t *testing.T) {
	op := NewAuthOperator(&config.Config{MooX: &config.MooxConfig{}})
	_, err := op.RegisterUser(context.Background(), "alice", "pwd", "nick", "a@b.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "认证服务地址未配置")
}

func TestAuthOperator_NewAuthOperator_ValidConfig_ShouldStoreConfig(t *testing.T) {
	cfg := &config.Config{MooX: &config.MooxConfig{AuthTarget: "127.0.0.1:9001"}}
	op := NewAuthOperator(cfg)
	assert.Equal(t, cfg, op.cfg)
}
