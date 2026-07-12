// Package rpc registers the HostAgentMgr tRPC service.
package rpc

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"trpc.group/trpc-go/trpc-go/server"
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
