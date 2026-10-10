package client

import (
	"context"
	"fmt"
	"slices"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// SyncHostPlacements sends the complete desired business component list for one
// host. Admin injects host components and preserves persisted enablement state.
func (c *Client) SyncHostPlacements(ctx context.Context, snapshot *setupconfig.Snapshot, hostID string) error {
	if snapshot == nil {
		return fmt.Errorf("setup snapshot is required")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return fmt.Errorf("config_changed")
	}
	host := snapshot.Manifest.HostByID(hostID)
	if !servicecatalog.ValidHostID(hostID) || !slices.ContainsFunc(snapshot.Manifest.Hosts(), func(h setupconfig.Host) bool { return h.Name == hostID }) {
		return fmt.Errorf("unknown deployment host %q", hostID)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	components := slices.Clone(snapshot.Manifest.Placements[hostID])
	for _, id := range components {
		component, ok := catalog.Component(id)
		if !ok || component.Scope == servicecatalog.ScopeHost {
			return fmt.Errorf("invalid business component %q", id)
		}
	}
	slices.Sort(components)
	response := &pb.SyncHostPlacementsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SyncHostPlacements", &pb.SyncHostPlacementsReq{
		HostId: hostID, Address: host.Address, PrivateAddress: host.PrivateAddress,
		Region: host.Region, ComponentIds: components,
	}, response); err != nil {
		return err
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return fmt.Errorf("config_changed")
	}
	return nil
}
