package broker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestLoadBoundedResponsePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	body := `users:
  - username: factor-control
    password: test-only
    permissions:
      publish: {allow: []}
      subscribe: {allow: ["moox.factor.internal.catalog.snapshot"]}
      responses: {max_messages: 1, expires: 10s}
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	users, err := loadUsersFile(path)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, 1, users[0].Permissions.Response.MaxMsgs)
	require.Equal(t, 10*time.Second, users[0].Permissions.Response.Expires)
	require.Empty(t, users[0].Permissions.Publish.Allow)
	require.NotNil(t, users[0].Permissions.Publish.Allow)
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, Users: append(users, &natsserver.User{Username: "engine", Password: "test-only"})})
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	require.True(t, srv.ReadyForConnections(5*time.Second))
	denied := make(chan error, 4)
	control, err := nats.Connect(srv.ClientURL(), nats.UserInfo("factor-control", "test-only"), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { denied <- err }))
	require.NoError(t, err)
	defer control.Close()
	_, err = control.Subscribe("moox.factor.internal.catalog.snapshot", func(msg *nats.Msg) { _ = msg.Respond([]byte("snapshot")) })
	require.NoError(t, err)
	require.NoError(t, control.Flush())
	engine, err := nats.Connect(srv.ClientURL(), nats.UserInfo("engine", "test-only"))
	require.NoError(t, err)
	defer engine.Close()
	message, err := engine.Request("moox.factor.internal.catalog.snapshot", nil, time.Second)
	require.NoError(t, err)
	require.Equal(t, "snapshot", string(message.Data))
	require.NoError(t, control.Publish("_INBOX.unrelated", []byte("forbidden")))
	require.NoError(t, control.Flush())
	select {
	case err := <-denied:
		require.ErrorContains(t, err, "Permissions Violation")
	case <-time.After(time.Second):
		t.Fatal("unsolicited publish was not denied")
	}
	invalid := `users:
  - username: factor-control
    password: test-only
    permissions:
      responses: {max_messages: -1, expires: 10s}
`
	require.NoError(t, os.WriteFile(path, []byte(invalid), 0o600))
	_, err = loadUsersFile(path)
	require.ErrorContains(t, err, "response limits")
}
