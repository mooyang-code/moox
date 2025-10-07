package nodeservice

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/nodeservice/heartbeat"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudNodeService 云节点服务实现
type CloudNodeService struct {
	heartbeatManager *heartbeat.Manager
}

// NewCloudNodeService 创建云节点服务
func NewCloudNodeService(heartbeatManager *heartbeat.Manager) *CloudNodeService {
	return &CloudNodeService{
		heartbeatManager: heartbeatManager,
	}
}

// Heartbeat 节点心跳上报
func (s *CloudNodeService) Heartbeat(ctx context.Context, req *pb.HeartbeatReq) (*pb.HeartbeatRsp, error) {
	// 参数验证
	if req.NodeId == "" {
		return &pb.HeartbeatRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "node_id is required",
			},
			Success: false,
		}, nil
	}

	// 转换proto消息到内部数据结构
	heartbeatData := heartbeat.HeartbeatData{
		NodeID:       req.NodeId,
		Timestamp:    time.Unix(req.Timestamp, 0),
		Status:       req.Status,
		RunningTasks: make([]heartbeat.RunningTaskInfo, len(req.RunningTasks)),
	}

	// 转换运行中的任务信息
	for i, task := range req.RunningTasks {
		heartbeatData.RunningTasks[i] = heartbeat.RunningTaskInfo{
			TaskID:        task.TaskId,
			CollectorType: task.CollectorType,
			Source:        task.Source,
			StartTime:     time.Unix(task.StartTime, 0),
			LastExecTime:  time.Unix(task.LastExecTime, 0),
			ExecCount:     task.ExecCount,
			ErrorCount:    task.ErrorCount,
		}
	}

	// 处理心跳
	resp, err := s.heartbeatManager.HandleHeartbeat(ctx, heartbeatData)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNodeService] Failed to handle heartbeat: %v", err)
		return &pb.HeartbeatRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "Failed to handle heartbeat",
			},
			Success: false,
		}, nil
	}

	// 返回成功响应
	return &pb.HeartbeatRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "success",
		},
		Success:   resp.Success,
		Timestamp: resp.Timestamp.Unix(),
	}, nil
}

// GetNodeStatus 获取节点状态
func (s *CloudNodeService) GetNodeStatus(ctx context.Context, req *pb.GetNodeStatusReq) (*pb.GetNodeStatusRsp, error) {
	nodes := make([]*pb.NodeStatusInfo, 0)

	// 如果指定了节点ID列表，只返回指定的节点
	if len(req.NodeIds) > 0 {
		for _, nodeID := range req.NodeIds {
			if nodeInfo := s.getNodeStatusInfo(nodeID); nodeInfo != nil {
				nodes = append(nodes, nodeInfo)
			}
		}
	} else {
		// 返回所有节点状态
		s.heartbeatManager.NodeStates.Range(func(key, value interface{}) bool {
			nodeID := key.(string)
			if nodeInfo := s.getNodeStatusInfo(nodeID); nodeInfo != nil {
				nodes = append(nodes, nodeInfo)
			}
			return true
		})
	}

	return &pb.GetNodeStatusRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "success",
		},
		Nodes: nodes,
	}, nil
}

// getNodeStatusInfo 获取单个节点状态信息
func (s *CloudNodeService) getNodeStatusInfo(nodeID string) *pb.NodeStatusInfo {
	val, exists := s.heartbeatManager.NodeStates.Load(nodeID)
	if !exists {
		return nil
	}

	state := val.(heartbeat.NodeState)

	// 构建节点状态信息
	nodeInfo := &pb.NodeStatusInfo{
		NodeId:           nodeID,
		Status:           int32(state.Status),
		LastHeartbeat:    state.LastHeartbeat.Unix(),
		RunningTaskCount: int32(state.RunningTasks),
		RunningTasks:     make([]*pb.RunningTaskInfo, 0),
	}

	// TODO: 可以从数据库读取更详细的任务信息
	// 这里暂时只返回任务数量

	return nodeInfo
}
