package unitdeploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

type probeClient struct {
	setupssh.Client
	replies *[]string
}

func (c probeClient) Run(context.Context, []string, io.Reader) (setupssh.Result, error) {
	reply := (*c.replies)[0]
	*c.replies = (*c.replies)[1:]
	return setupssh.Result{Stdout: reply + "\n"}, nil
}
func (probeClient) Close() error { return nil }

func TestHelperFailureExplicitFailureDoesNotWait(t *testing.T) {
	err := helperFailure(context.Background(), func() (setupssh.Client, error) {
		t.Fatal("目标机明确失败时不需要重新连接")
		return nil, nil
	}, "部署管理台", errors.New("exit 1"), setupssh.Result{ExitCode: 1})
	require.ErrorContains(t, err, "target_rejected")
	require.NotContains(t, err.Error(), "断开")
}

func TestHelperFailureWaitsForTargetAfterDisconnect(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = time.Minute, time.Millisecond
	replies := []string{"running", "running", "idle"}
	redial := func() (setupssh.Client, error) { return probeClient{replies: &replies}, nil }
	err := helperFailure(context.Background(), redial, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.ErrorContains(t, err, "已结束")
	require.Empty(t, replies)
}

func TestHelperFailureGivesUpAfterDeadline(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = 0, time.Millisecond
	redial := func() (setupssh.Client, error) { return nil, errors.New("unreachable") }
	err := helperFailure(context.Background(), redial, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.True(t, strings.Contains(err.Error(), "可能还在运行"))
}
