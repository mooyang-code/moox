package command

import (
	"context"
	"io"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

type collectorBlacklistSSHStub struct {
	setupssh.Client
	result setupssh.Result
	err    error
	args   []string
}

func (s *collectorBlacklistSSHStub) Run(_ context.Context, args []string, _ io.Reader) (setupssh.Result, error) {
	s.args = args
	return s.result, s.err
}
