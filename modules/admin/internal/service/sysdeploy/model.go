package sysdeploy

import "time"

// Deployment 是系统服务部署信息，对应 t_service_deployments。
type Deployment struct {
	ID               int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	NodeID           string    `gorm:"column:c_node_id;not null;uniqueIndex:idx_service_deployments_node_name,priority:1;uniqueIndex:idx_service_deployments_node_gateway_service,priority:1,where:c_gateway_enabled = 1 AND c_gateway_service_id <> '';index" json:"node_id"`
	ServiceName      string    `gorm:"column:c_service_name;not null;uniqueIndex:idx_service_deployments_node_name,priority:2" json:"service_name"`
	ServiceKind      string    `gorm:"column:c_service_kind;not null;default:''" json:"service_kind"`
	Protocol         string    `gorm:"column:c_protocol;not null;default:'http'" json:"protocol"`
	Host             string    `gorm:"column:c_host;not null;default:''" json:"host"`
	Port             int32     `gorm:"column:c_port;not null;default:0" json:"port"`
	GatewayPath      string    `gorm:"column:c_gateway_path;not null;default:''" json:"gateway_path"`
	GatewayServiceID string    `gorm:"column:c_gateway_service_id;not null;default:'';uniqueIndex:idx_service_deployments_node_gateway_service,priority:2,where:c_gateway_enabled = 1 AND c_gateway_service_id <> ''" json:"gateway_service_id"`
	GatewayEnabled   bool      `gorm:"column:c_gateway_enabled;not null;default:false" json:"gateway_enabled"`
	Scope            string    `gorm:"column:c_scope;not null;default:'public'" json:"scope"`
	Status           string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Description      string    `gorm:"column:c_description;not null;default:''" json:"description"`
	ExtraConfig      string    `gorm:"column:c_extra_config;not null;default:'{}'" json:"extra_config"`
	CreatedAt        time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (Deployment) TableName() string { return "t_service_deployments" }

type GatewayNode struct {
	NodeID           string     `gorm:"column:c_node_id;primaryKey" json:"node_id"`
	HostID           *int64     `gorm:"column:c_host_id" json:"host_id"`
	Name             string     `gorm:"column:c_name;not null" json:"name"`
	PublicAddress    string     `gorm:"column:c_public_address;not null" json:"public_address"`
	Status           string     `gorm:"column:c_status;not null" json:"status"`
	RouteHash        string     `gorm:"column:c_route_hash;not null" json:"route_hash"`
	AppliedRouteHash string     `gorm:"column:c_applied_route_hash;not null" json:"applied_route_hash"`
	RouteCount       int32      `gorm:"column:c_route_count;not null" json:"route_count"`
	LastSeenAt       *time.Time `gorm:"column:c_last_seen_at" json:"last_seen_at"`
	LastError        string     `gorm:"column:c_last_error;not null" json:"last_error"`
	CreatedAt        *time.Time `gorm:"column:c_ctime" json:"created_at"`
	UpdatedAt        *time.Time `gorm:"column:c_mtime" json:"updated_at"`
}

func (GatewayNode) TableName() string { return "t_gateway_nodes" }
