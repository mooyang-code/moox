// Package healthview 生成健康概览 v2：组件、数据链路、业务检查、主机、未登记进程和当前告警。组件名称只取自组件目录，
// 状态统一为六种，后端给出中文摘要并保留原始错误。
package healthview

import "time"

// 健康概览统一的状态值。
const (
	StatusHealthy   = "healthy"
	StatusDegraded  = "degraded"
	StatusDown      = "down"
	StatusUnknown   = "unknown"
	StatusDisabled  = "disabled"
	StatusUnchecked = "unchecked"
)

// 告警对象的类型。
const (
	TargetComponent = "component"
	TargetDataset   = "dataset"
	TargetHost      = "host"
	TargetBusiness  = "business"
)

// 告警级别：主机资源阈值告警为 warning，其余为 critical。
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// 数据链路的阶段。
const (
	StageCollect = "collect"
	StageStorage = "storage"
	StageFactor  = "factor"
	StageTrade   = "trade"
)

// Overview 是健康概览 v2。
type Overview struct {
	GeneratedAt    time.Time
	Summary        Summary
	Alerts         []Alert
	Components     []Component
	DataStages     []DataStage
	BusinessChecks []BusinessCheck
	Hosts          []Host
	Unregistered   []Unregistered
	Notification   Notification
	// Warnings 是数据来源暂时不可用等需要提示的情况。
	Warnings []string
}

// Summary 是概览计数：告警数，以及组件、主机、业务检查、数据集中需关注（降级或故障）、正常、未知的数量。
type Summary struct {
	Alerts, Attention, Healthy, Unknown int
}

// Target 是告警对象。
type Target struct {
	Kind, HostID, ComponentID, SpaceID, DatasetID, Frequency string
}

// Alert 是一条正在触发的告警；Stage 是它所属的数据链路阶段，不属于数据链路时为空。
type Alert struct {
	ID, Severity, Title, Reason, RawError, Stage string
	Target                                       Target
	TriggeredAt, LastCheckedAt                   time.Time
}

// Probe 是一次健康探测的结果。
type Probe struct {
	Status, URL, RawError string
	CheckedAt             time.Time
}

// Reporter 是组件的运行指标上报。
type Reporter struct {
	Status, InstanceID, Version string
	LastSeenAt                  time.Time
}

// Component 是部署在一台主机上的一个组件。
type Component struct {
	HostID, ComponentID, Name, Status, Reason string
	Probe                                     Probe
	History                                   []Probe
	Reporter                                  Reporter
	StatusSince                               time.Time
}

// StageDataset 是数据链路中的一个数据集频率。
type StageDataset struct {
	SpaceID, DatasetID, Frequency, Producer, Status, Reason, RawError string
	WatermarkAt, LastSuccessAt                                        time.Time
	LagSeconds                                                        int64
}

// DataStage 是数据链路的一个阶段。
type DataStage struct {
	Stage, Name, Status string
	Datasets            []StageDataset
}

// BusinessCheck 是一项业务检查；Stage 是它所属的数据链路阶段，不属于数据链路时为空。
type BusinessCheck struct {
	Kind, Name, Module, SpaceID, Status, Reason, RawError, Stage string
	CheckedAt                                                    time.Time
}

// Host 是一台主机的摘要。
type Host struct {
	HostID, AgentID, Status, Reason, GatewayState string
	CPUPercent, MemoryPercent, DiskPercent        float64
	LastSeenAt                                    time.Time
}

// Unregistered 是在上报运行指标、但没有登记部署的进程。
type Unregistered struct {
	HostID, ComponentID, InstanceID, Version string
	LastSeenAt                               time.Time
}

// Notification 是告警推送渠道的配置状态。
type Notification struct {
	ChannelType   string
	Configured    bool
	WebhookMasked string
}
