package sysdeploy

import "time"

// Deployment 是系统服务部署信息，对应 t_service_deployments。
type Deployment struct {
	ID          int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	ServiceName string    `gorm:"column:c_service_name;not null;uniqueIndex:idx_service_deployments_name_deleted" json:"service_name"`
	ServiceKind string    `gorm:"column:c_service_kind;not null;default:''" json:"service_kind"`
	Protocol    string    `gorm:"column:c_protocol;not null;default:'http'" json:"protocol"`
	Host        string    `gorm:"column:c_host;not null;default:''" json:"host"`
	Port        int32     `gorm:"column:c_port;not null;default:0" json:"port"`
	BaseURL     string    `gorm:"column:c_base_url;not null;default:''" json:"base_url"`
	RPCAddress  string    `gorm:"column:c_rpc_address;not null;default:''" json:"rpc_address"`
	GatewayPath string    `gorm:"column:c_gateway_path;not null;default:''" json:"gateway_path"`
	Scope       string    `gorm:"column:c_scope;not null;default:'public'" json:"scope"`
	Status      string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Description string    `gorm:"column:c_description;not null;default:''" json:"description"`
	ExtraConfig string    `gorm:"column:c_extra_config;not null;default:'{}'" json:"extra_config"`
	IsDeleted   string    `gorm:"column:c_is_deleted;not null;default:'false';uniqueIndex:idx_service_deployments_name_deleted" json:"-"`
	CreatedAt   time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (Deployment) TableName() string { return "t_service_deployments" }
