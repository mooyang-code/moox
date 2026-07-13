// Package rpc registers the HostAgentMgr tRPC service.
package rpc

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

const HostAgentMgrServiceName = "trpc.moox.hostagent.HostAgentMgr"

// Register wires HostAgentMgr onto the configured tRPC service.
func Register(s *server.Server, a *app.Agent) error {
	if s == nil || a == nil {
		return fmt.Errorf("server and agent are required")
	}
	svc := s.Service(HostAgentMgrServiceName)
	if svc == nil {
		return fmt.Errorf("hostagent service is not configured")
	}
	hostagentpb.RegisterHostAgentMgrService(svc, a)
	return nil
}

var _ hostagentpb.HostAgentMgrService = (*app.Agent)(nil)
