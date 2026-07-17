package app

import (
	"context"
	"fmt"
	"time"

	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

const sampleTimerService = "trpc.moox.hostagent.sample.timer"

type sampleHandler struct {
	timeout  time.Duration
	shutdown context.Context
	run      func(context.Context) (*hostagentpb.RunOnceRsp, error)
}

func newSampleHandler(timeout time.Duration, run func(context.Context) (*hostagentpb.RunOnceRsp, error)) (*sampleHandler, error) {
	return newSampleHandlerWithShutdown(timeout, trpc.BackgroundContext(), run)
}

func newSampleHandlerWithShutdown(timeout time.Duration, shutdown context.Context, run func(context.Context) (*hostagentpb.RunOnceRsp, error)) (*sampleHandler, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("hostagent sample timeout must be positive")
	}
	if run == nil {
		return nil, fmt.Errorf("hostagent sample callback is required")
	}
	if shutdown == nil {
		return nil, fmt.Errorf("hostagent sample shutdown context is required")
	}
	return &sampleHandler{timeout: timeout, shutdown: shutdown, run: run}, nil
}

func (h *sampleHandler) Handle(ctx context.Context) error {
	runCtx := trpc.CloneContext(ctx)
	runCtx, cancel := context.WithTimeout(runCtx, h.timeout)
	defer cancel()
	stopShutdownLink := context.AfterFunc(h.shutdown, cancel)
	defer stopShutdownLink()
	_, err := h.run(runCtx)
	if err == nil && runCtx.Err() != nil {
		return runCtx.Err()
	}
	return err
}

// RegisterSampleTimer registers the single scheduling owner for HostAgent sampling.
func RegisterSampleTimer(s *server.Server, agent *Agent) error {
	if s == nil {
		return fmt.Errorf("hostagent sample timer requires a tRPC server")
	}
	if agent == nil {
		return fmt.Errorf("hostagent sample timer requires an agent")
	}
	shutdownCtx, cancelShutdown := context.WithCancel(trpc.BackgroundContext())
	s.RegisterOnShutdown(cancelShutdown)
	handler, err := newSampleHandlerWithShutdown(30*time.Second, shutdownCtx, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		return agent.runOnceGuarded(ctx)
	})
	if err != nil {
		return err
	}
	return registerSampleHandler(s, handler.Handle)
}

func registerSampleHandler(s *server.Server, handler func(context.Context) error) error {
	service := s.Service(sampleTimerService)
	if service == nil {
		return fmt.Errorf("hostagent sample timer service %q is not configured", sampleTimerService)
	}
	timer.RegisterHandlerService(service, handler)
	return nil
}
