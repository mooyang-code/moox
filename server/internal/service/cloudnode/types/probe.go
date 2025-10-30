package types

import "time"

// ProbeLog 探测日志
type ProbeLog struct {
	ID              int64                  `json:"id" gorm:"column:c_id;primaryKey"`
	ProbeID         string                 `json:"probe_id" gorm:"column:c_probe_id"`
	NodeID          string                 `json:"node_id" gorm:"column:c_node_id"`
	NodeType        string                 `json:"node_type" gorm:"column:c_node_type"`
	ProbeTime       time.Time              `json:"probe_time" gorm:"column:c_probe_time"`
	ProbeURL        string                 `json:"probe_url" gorm:"column:c_probe_url"`
	ProbeMethod     string                 `json:"probe_method" gorm:"column:c_probe_method"`
	ProbeTimeout    int                    `json:"probe_timeout" gorm:"column:c_probe_timeout"`
	Result          bool                   `json:"result" gorm:"column:c_result"`
	StatusCode      int                    `json:"status_code" gorm:"column:c_status_code"`
	ResponseTime    int                    `json:"response_time" gorm:"column:c_response_time"`
	ErrorMessage    string                 `json:"error_message" gorm:"column:c_error_message"`
	AdapterType     string                 `json:"adapter_type" gorm:"column:c_adapter_type"`
	StrategyName    string                 `json:"strategy_name" gorm:"column:c_strategy_name"`
	RequestDetails  map[string]interface{} `json:"request_details" gorm:"column:c_request_details;type:text"`
	ResponseDetails map[string]interface{} `json:"response_details" gorm:"column:c_response_details;type:text"`
	Invalid         int                    `json:"-" gorm:"column:c_invalid"`
	CreateTime      time.Time              `json:"created_time" gorm:"column:c_ctime;autoCreateTime"`
}

func (ProbeLog) TableName() string {
	return "t_heartbeat_probe_logs"
}

// ProbeResult 探测结果
type ProbeResult struct {
	ProbeID         string `json:"probe_id"`
	CostTime        int    `json:"cost_time"` // 探测耗时（毫秒）
	ErrorMessage    string `json:"error_message"`
	RequestID       string `json:"request_id"`
	NodeID          string `json:"node_id"`
	State           string `json:"state"`
	RemoteTimestamp string `json:"remote_timestamp"` // 远端上报的时间戳
	OSName          string `json:"os"`
	FunctionVersion string `json:"function_version"`
	ProbeTime       int64  `json:"probe_time"` // 本地探测时间戳（毫秒）
}

// ProbeLogFilter 探测日志过滤器
type ProbeLogFilter struct {
	NodeID    string     `json:"node_id"`
	NodeType  string     `json:"node_type"`
	ProbeID   string     `json:"probe_id"`
	Result    *bool      `json:"result"`
	StartTime *time.Time `json:"start_time"`
	EndTime   *time.Time `json:"end_time"`
	Page      int        `json:"page"`
	PageSize  int        `json:"page_size"`
}

// GetPage 获取页码
func (f *ProbeLogFilter) GetPage() int {
	if f.Page <= 0 {
		return 1
	}
	return f.Page
}

// GetPageSize 获取页大小
func (f *ProbeLogFilter) GetPageSize() int {
	if f.PageSize <= 0 {
		return 20
	}
	if f.PageSize > 100 {
		return 100
	}
	return f.PageSize
}