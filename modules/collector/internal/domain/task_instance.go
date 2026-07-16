package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	InstanceStatusPending    = 1
	InstanceStatusRunning    = 2
	InstanceStatusSuccess    = 3
	InstanceStatusPartFailed = 4
	InstanceStatusFailed     = 5
)

// DatasetSubject is a storage dataset subject projected into Collector.
type DatasetSubject struct {
	SubjectID      string
	SubjectName    string
	ExternalSymbol string
	Status         string
}

// TaskSpec is an adapter output before persistence fields are added.
type TaskSpec struct {
	Exchange  string
	Market    string
	DataType  string
	DatasetID string
	SubjectID string
	Symbol    string
	Interval  string
	Params    map[string]any
}

// TaskInstance is the Collector-owned executable business task.
type TaskInstance struct {
	ID             int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string     `gorm:"column:c_space_id"`
	TaskID         string     `gorm:"column:c_task_id"`
	CloudJobItemID string     `gorm:"column:c_cloud_job_item_id"`
	RuleID         string     `gorm:"column:c_rule_id"`
	Exchange       string     `gorm:"column:c_exchange"`
	Market         string     `gorm:"column:c_market"`
	DataType       string     `gorm:"column:c_data_type"`
	DatasetID      string     `gorm:"column:c_dataset_id"`
	SubjectID      string     `gorm:"column:c_subject_id"`
	Symbol         string     `gorm:"column:c_symbol"`
	Interval       string     `gorm:"column:c_interval"`
	LastExecNode   string     `gorm:"column:c_last_exec_node"`
	LastExecStatus int        `gorm:"column:c_last_exec_status"`
	TaskParams     string     `gorm:"column:c_task_params"`
	LastExecTime   *time.Time `gorm:"column:c_last_exec_time"`
	Result         string     `gorm:"column:c_result"`
	IsDeleted      bool       `gorm:"column:c_is_deleted"`
	CreateTime     time.Time  `gorm:"column:c_ctime"`
	ModifyTime     time.Time  `gorm:"column:c_mtime"`
}

// TableName returns the Collector task instance table.
func (i *TaskInstance) TableName() string {
	return "t_collector_task_instances"
}

// StableTaskID creates an idempotent task ID for a rule/object/interval.
func StableTaskID(spaceID string, ruleID string, spec TaskSpec) string {
	parts := []string{
		spaceID,
		ruleID,
		spec.Exchange,
		spec.Market,
		spec.DataType,
		spec.DatasetID,
		spec.SubjectID,
		spec.Interval,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:32]
}
