package conn

import (
	"testing"
	"time"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionManager_StoreLoad_RoundTrip_ShouldWork(t *testing.T) {
	mgr := NewSessionManager()
	conn := &SSHConn{
		SessionID: "s1",
		Target:    model.Target{HostID: "storage", Address: "10.0.0.1", Port: 22, User: "root"},
		StartTime: time.Now(),
	}
	mgr.Store("s1", conn)

	got, ok := mgr.Load("s1")
	require.True(t, ok)
	assert.Equal(t, "s1", got.SessionID)
}

func TestSessionManager_Delete_RemovesSession_ShouldNotLoad(t *testing.T) {
	mgr := NewSessionManager()
	conn := &SSHConn{SessionID: "s2", Target: model.Target{HostID: "storage"}}
	mgr.Store("s2", conn)
	mgr.Delete("s2")

	_, ok := mgr.Load("s2")
	assert.False(t, ok)
}

func TestSessionManager_Count_MultipleSessions_ShouldMatch(t *testing.T) {
	mgr := NewSessionManager()
	mgr.Store("a", &SSHConn{SessionID: "a", Target: model.Target{HostID: "storage"}})
	mgr.Store("b", &SSHConn{SessionID: "b", Target: model.Target{HostID: "overseas"}})
	assert.Equal(t, 2, mgr.Count())
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
		Target: model.Target{
			HostID: "storage", Address: "10.0.0.4", Port: 22, User: "dba", Credential: secretmodel.SSHCredential{Password: "secret"},
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

func TestSessionManager_DeleteByHost_ClosesOnlyThatHost(t *testing.T) {
	mgr := NewSessionManager()
	mgr.Store("a", &SSHConn{SessionID: "a", Target: model.Target{HostID: "storage"}})
	mgr.Store("b", &SSHConn{SessionID: "b", Target: model.Target{HostID: "storage"}})
	mgr.Store("c", &SSHConn{SessionID: "c", Target: model.Target{HostID: "overseas"}})
	assert.Equal(t, 2, mgr.DeleteByHost("storage"))
	assert.Equal(t, 1, mgr.Count())
	_, ok := mgr.Load("c")
	assert.True(t, ok)
	assert.Zero(t, mgr.DeleteByHost("storage"))
}
