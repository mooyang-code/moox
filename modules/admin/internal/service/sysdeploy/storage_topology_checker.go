package sysdeploy

import pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

func storageTopologyWarnings(serviceName string) []*pb.ServiceDeploymentWarning {
	if serviceName != "" && !isStorageDeployment(serviceName) {
		return nil
	}
	return []*pb.ServiceDeploymentWarning{{
		Code:            "storage_topology_overlap",
		ServiceName:     serviceName,
		RelatedEndpoint: "/#/ops/storage/nodes",
		Message:         "storage_* 部署信息只描述服务访问地址，不会自动修改主存拓扑；如果变更 storage 服务 IP/端口，请同步检查主存节点 Endpoint。",
	}}
}

func isStorageDeployment(serviceName string) bool {
	switch serviceName {
	case "storage_metadata", "storage_access", "storage_view",
		"storage_metadata_trpc", "storage_primary_trpc", "storage_access_trpc", "storage_view_trpc":
		return true
	default:
		return false
	}
}
