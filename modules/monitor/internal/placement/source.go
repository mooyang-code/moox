// Package placement discovers Monitor checks from the shared catalog and
// SysDeploy v2. All RPCs borrow the process-owned gateway client.
package placement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const pageSize = 100
const maxRecords = 1500

type Snapshot struct {
	Catalog    servicecatalog.Catalog
	Hosts      []*adminpb.DeploymentHost
	Placements []*adminpb.ComponentPlacement
}

type Source interface {
	Snapshot(context.Context) (Snapshot, error)
}

type ClientSource struct{ gateway gatewayclient.Invoker }

func NewClientSource(gateway gatewayclient.Invoker) *ClientSource {
	return &ClientSource{gateway: gateway}
}

func (s *ClientSource) invoke(ctx context.Context, method string, request, response any) error {
	if s == nil || s.gateway == nil {
		return fmt.Errorf("placement discovery requires the process gateway client")
	}
	return s.gateway.Invoke(ctx, "trpc.moox.ops.SysDeploy", method, request, response)
}

func responseError(info *commonpb.RetInfo) error {
	if info == nil || info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("SysDeploy v2 unavailable: %s", info.GetMsg())
	}
	return nil
}

func (s *ClientSource) Snapshot(ctx context.Context) (Snapshot, error) {
	var response adminpb.GetCatalogRsp
	if err := s.invoke(ctx, "GetCatalog", &adminpb.GetCatalogReq{}, &response); err != nil {
		return Snapshot{}, err
	}
	if err := responseError(response.GetRetInfo()); err != nil {
		return Snapshot{}, err
	}
	raw := []byte(response.GetCatalogYaml())
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != response.GetSha256() {
		return Snapshot{}, fmt.Errorf("SysDeploy component catalog hash mismatch")
	}
	localHash := sha256.Sum256(servicecatalog.EmbeddedYAML())
	if hash != localHash {
		return Snapshot{}, fmt.Errorf("SysDeploy catalog differs from this Monitor release")
	}
	catalog, err := servicecatalog.Decode(bytes.NewReader(raw))
	if err != nil {
		return Snapshot{}, err
	}
	hosts, err := s.hosts(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	placements, err := s.placements(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Catalog: catalog, Hosts: hosts, Placements: placements}, nil
}

func (s *ClientSource) hosts(ctx context.Context) ([]*adminpb.DeploymentHost, error) {
	var rows []*adminpb.DeploymentHost
	for page := uint32(1); page <= maxRecords/pageSize; page++ {
		var response adminpb.ListDeploymentHostsRsp
		if err := s.invoke(ctx, "ListHosts", &adminpb.ListDeploymentHostsReq{Page: &commonpb.Page{Page: page, Size: pageSize}}, &response); err != nil {
			return nil, err
		}
		if err := responseError(response.GetRetInfo()); err != nil {
			return nil, err
		}
		rows = append(rows, response.GetHosts()...)
		if len(rows) > maxRecords {
			break
		}
		if !response.GetPageResult().GetHasMore() {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("deployment hosts exceed limit %d", maxRecords)
}

func (s *ClientSource) placements(ctx context.Context) ([]*adminpb.ComponentPlacement, error) {
	var rows []*adminpb.ComponentPlacement
	for page := uint32(1); page <= maxRecords/pageSize; page++ {
		var response adminpb.ListPlacementsRsp
		if err := s.invoke(ctx, "ListPlacements", &adminpb.ListPlacementsReq{Page: &commonpb.Page{Page: page, Size: pageSize}}, &response); err != nil {
			return nil, err
		}
		if err := responseError(response.GetRetInfo()); err != nil {
			return nil, err
		}
		rows = append(rows, response.GetPlacements()...)
		if len(rows) > maxRecords {
			break
		}
		if !response.GetPageResult().GetHasMore() {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("component placements exceed limit %d", maxRecords)
}

func (s *ClientSource) GatewayStatus(ctx context.Context, hostID string) (*adminpb.HostGatewayRuntimeStatus, error) {
	var response adminpb.GetHostRoutesRsp
	if err := s.invoke(ctx, "GetHostRoutes", &adminpb.GetHostRoutesReq{HostId: hostID}, &response); err != nil {
		return nil, err
	}
	if err := responseError(response.GetRetInfo()); err != nil {
		return nil, err
	}
	if response.GetHostId() != hostID {
		return nil, fmt.Errorf("gateway status host does not match %s", hostID)
	}
	return response.GetGatewayStatus(), nil
}
