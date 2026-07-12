package conn

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionManager_StoreLoad_RoundTrip_ShouldWork(t *testing.T) {
	mgr := NewSessionManager()
	conn := &SSHConn{
		SessionID: "s1",
		Host:      &model.SSHHost{ID: 1, Name: "dev", Address: "10.0.0.1", Port: 22, User: "root"},
		StartTime: time.Now(),
	}
	mgr.Store("s1", conn)

	got, ok := mgr.Load("s1")
	require.True(t, ok)
	assert.Equal(t, "s1", got.SessionID)
}

func TestSessionManager_Delete_RemovesSession_ShouldNotLoad(t *testing.T) {
	mgr := NewSessionManager()
	conn := &SSHConn{SessionID: "s2", Host: &model.SSHHost{ID: 2}}
	mgr.Store("s2", conn)
	mgr.Delete("s2")

	_, ok := mgr.Load("s2")
	assert.False(t, ok)
}

func TestSessionManager_Count_MultipleSessions_ShouldMatch(t *testing.T) {
	mgr := NewSessionManager()
	mgr.Store("a", &SSHConn{SessionID: "a", Host: &model.SSHHost{}})
	mgr.Store("b", &SSHConn{SessionID: "b", Host: &model.SSHHost{}})
	assert.Equal(t, 2, mgr.Count())
}

func TestSessionManager_GetAllSessions_ShouldReturnInfo(t *testing.T) {
	mgr := NewSessionManager()
	now := time.Now()
	mgr.Store("s3", &SSHConn{
		SessionID:      "s3",
		ClientIP:       "127.0.0.1",
		StartTime:      now,
		LastActiveTime: now,
		Host:           &model.SSHHost{ID: 3, Name: "web", Address: "10.0.0.3", Port: 22, User: "ops"},
	})

	sessions := mgr.GetAllSessions()
	require.Len(t, sessions, 1)
	assert.Equal(t, "s3", sessions[0].SessionID)
	assert.Equal(t, "web", sessions[0].HostName)
}

func TestSSHConn_RefreshActiveTime_ShouldUpdateTimestamp(t *testing.T) {
	conn := &SSHConn{LastActiveTime: time.Now().Add(-time.Hour)}
	before := conn.LastActiveTime
	conn.RefreshActiveTime()
	assert.True(t, conn.LastActiveTime.After(before))
}

func TestSSHConn_MarshalJSON_ShouldHideSensitiveFields(t *testing.T) {
	conn := &SSHConn{
		SessionID: "s4",
		Host: &model.SSHHost{
			ID: 4, Name: "db", Address: "10.0.0.4", Port: 22, User: "dba", Password: "secret",
		},
		StartTime:      time.Now(),
		LastActiveTime: time.Now(),
		ClientIP:       "1.2.3.4",
	}
	data, err := conn.MarshalJSON()
	require.NoError(t, err)
	body := string(data)
	assert.Contains(t, body, "session_id")
	assert.NotContains(t, body, "secret")
}

func TestSSHConn_ResizeWindow_NoSession_ShouldError(t *testing.T) {
	conn := &SSHConn{}
	err := conn.ResizeWindow(80, 24)
	require.Error(t, err)
}

// Connect / RunTerminal 需要真实 SSH 服务端，单测环境无法稳定执行，跳过。
