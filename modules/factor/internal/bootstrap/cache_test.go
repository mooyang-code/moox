package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

type cacheRegisteredService struct {
	value any
	err   error
}

func (*cacheRegisteredService) ServiceName() string               { return cacheTimerService }
func (s *cacheRegisteredService) Register(_ any, value any) error { s.value = value; return s.err }
func (*cacheRegisteredService) Serve() error                      { return nil }
func (*cacheRegisteredService) Close(chan struct{}) error         { return nil }

func TestRegisterEngineCacheUsesOwnedJobAndRejectsMissingService(t *testing.T) {
	require.NoError(t, registerEngineCache(nil, nil))
	cfg := inputcache.DefaultConfig()
	cfg.Dir = t.TempDir()
	r, err := inputcache.NewRuntime(context.Background(), cfg, time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	require.Error(t, registerEngineCache(nil, r))
	s := &server.Server{}
	require.Error(t, registerEngineCache(s, r))
	service := &cacheRegisteredService{}
	s.AddService(cacheTimerService, service)
	require.NoError(t, registerEngineCache(s, r))
	require.Same(t, r.Job, service.value)
	failure := errors.New("registration failed")
	service.err = failure
	require.ErrorIs(t, registerEngineCache(s, r), failure)
}
