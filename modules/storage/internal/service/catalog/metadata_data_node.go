package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const dataNodeRegistrationAppID = "storage-deployer"

func (s *Service) RegisterDataNode(ctx context.Context, req *pb.RegisterDataNodeReq) (*pb.RegisterDataNodeRsp, error) {
	if req == nil || strings.TrimSpace(req.GetNodeId()) == "" || strings.TrimSpace(req.GetServiceTarget()) == "" {
		return &pb.RegisterDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id and service_target are required"))}, nil
	}
	if err := s.validateDataNodeRegistrationAuth(req.GetAuthInfo()); err != nil {
		return &pb.RegisterDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	node, err := s.metadata.RegisterDataNode(ctx, req.GetNodeId(), req.GetServiceTarget(), req.GetInitialName())
	if err != nil {
		return &pb.RegisterDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "RegisterDataNode")
	return &pb.RegisterDataNodeRsp{RetInfo: retinfo.Success("success"), Node: node}, nil
}

func (s *Service) validateDataNodeRegistrationAuth(auth *pb.AuthInfo) error {
	if strings.TrimSpace(s.nodeAuthSecret) == "" {
		return errors.New("storage DataNode auth secret is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("service auth is required")
	}
	if auth.GetAppId() != dataNodeRegistrationAppID {
		return errors.New("invalid service app_id")
	}
	expected := serviceAuthKey(s.nodeAuthSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(expected)) {
		return errors.New("invalid service HMAC")
	}
	return nil
}

func serviceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) UpdateDataNode(ctx context.Context, req *pb.UpdateDataNodeReq) (*pb.UpdateDataNodeRsp, error) {
	if req == nil || strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.UpdateDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id is required"))}, nil
	}
	node, err := s.metadata.UpdateDataNode(ctx, req.GetNodeId(), req.GetName(), req.GetStatus())
	if err != nil {
		return &pb.UpdateDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "UpdateDataNode")
	return &pb.UpdateDataNodeRsp{RetInfo: retinfo.Success("success"), Node: node}, nil
}

func (s *Service) GetDataNode(ctx context.Context, req *pb.GetDataNodeReq) (*pb.GetDataNodeRsp, error) {
	if req == nil || strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.GetDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id is required"))}, nil
	}
	node, err := s.metadata.GetDataNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetDataNodeRsp{RetInfo: retinfo.Success("success"), Node: node}, nil
}

func (s *Service) ListDataNodes(ctx context.Context, req *pb.ListDataNodesReq) (*pb.ListDataNodesRsp, error) {
	var requestedStatus string
	var page *pb.Page
	if req != nil {
		requestedStatus = strings.TrimSpace(req.GetStatus())
		page = req.GetPage()
	}
	// The metadata Reader intentionally keeps a status-free query surface. Fetch
	// every underlying page before filtering so PageResult describes the filtered
	// result rather than the first unfiltered database page.
	nodes, err := s.listAllDataNodes(ctx)
	if err != nil {
		return &pb.ListDataNodesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	filtered := make([]*pb.DataNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || (requestedStatus != "" && node.GetStatus() != requestedStatus) {
			continue
		}
		filtered = append(filtered, node)
	}
	nodes, pageResult := pageSlice(filtered, page)
	items := make([]*pb.DataNodeListItem, 0, len(nodes))
	nodeIDs := make([]string, 0, len(nodes))
	byNodeID := make(map[string]*pb.DataNodeListItem, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		item := &pb.DataNodeListItem{Node: node, Datasets: make([]*pb.DatasetSummary, 0)}
		items = append(items, item)
		nodeIDs = append(nodeIDs, node.GetNodeId())
		byNodeID[node.GetNodeId()] = item
	}
	if len(nodeIDs) > 0 {
		datasets, _, err := s.metadata.ListDatasets(ctx, metadata.DatasetQuery{DataNodeIDs: nodeIDs})
		if err != nil {
			return &pb.ListDataNodesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
		}
		for _, dataset := range datasets {
			if dataset == nil {
				continue
			}
			item := byNodeID[dataset.GetDataNodeId()]
			if item == nil {
				continue
			}
			item.Datasets = append(item.Datasets, &pb.DatasetSummary{
				SpaceId:      dataset.GetSpaceId(),
				DatasetId:    dataset.GetDatasetId(),
				Name:         dataset.GetName(),
				DataKind:     dataset.GetDataKind(),
				KeepDuration: dataset.GetKeepDuration(),
				Status:       dataset.GetStatus(),
			})
		}
	}
	return &pb.ListDataNodesRsp{RetInfo: retinfo.Success("success"), Items: items, PageResult: pageResult}, nil
}

func (s *Service) listAllDataNodes(ctx context.Context) ([]*pb.DataNode, error) {
	var nodes []*pb.DataNode
	var page *pb.Page
	for {
		items, result, err := s.metadata.ListDataNodes(ctx, page)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, items...)
		if result == nil || !result.GetHasMore() {
			return nodes, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pageNo := result.GetPage()
		if pageNo == ^uint32(0) {
			return nil, errors.New("data node page overflow")
		}
		size := result.GetSize()
		if size == 0 {
			size = 1000
		}
		page = &pb.Page{Page: pageNo + 1, Size: size}
	}
}

func (s *Service) DeleteDataNode(ctx context.Context, req *pb.DeleteDataNodeReq) (*pb.DeleteDataNodeRsp, error) {
	if req == nil || strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.DeleteDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id is required"))}, nil
	}
	node, err := s.metadata.GetDataNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.DeleteDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.metadata.DeleteDataNode(ctx, req.GetNodeId()); err != nil {
		return &pb.DeleteDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteDataNode")
	return &pb.DeleteDataNodeRsp{RetInfo: retinfo.Success("success"), Node: node}, nil
}
