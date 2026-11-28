// Package dataformat 在部署预检阶段计算当前版本各组件期望的数据格式，并与目标机上的 data-format 比对。
package dataformat

import (
	"fmt"
	"slices"
	"strconv"

	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	archiveschema "github.com/mooyang-code/moox/modules/archive/schema"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	monitorschema "github.com/mooyang-code/moox/modules/monitor/schema"
	storageschema "github.com/mooyang-code/moox/modules/storage/schema"
	strategyschema "github.com/mooyang-code/moox/modules/strategy/schema"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	pkgdataformat "github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/sqlitedb"
)

// statefulComponents 按固定顺序列出全部 12 个需要参与数据格式比对的有状态组件。
// host-gateway、host-agent、egress-proxy、collector-stream 无需要保留的持久化状态，不在此列。
var statefulComponents = []string{
	"admin",
	"monitor",
	"collector",
	"factor-mgr",
	"archive-mgr",
	"strategy",
	"trade",
	"storage-primary",
	"storage-view",
	"eventbus",
	"factor-engine",
	"archive-worker",
}

// StatefulComponents 返回全部参与数据格式比对的有状态组件 ID 列表（副本）。
func StatefulComponents() []string {
	return slices.Clone(statefulComponents)
}

// IsStateful 报告组件 ID 是否参与数据格式比对。
func IsStateful(component string) bool {
	return slices.Contains(statefulComponents, component)
}

// Inputs 是计算全部有状态组件期望格式所需的全部输入（便于单测替换单项输入验证敏感度）。
type Inputs struct {
	AdminSQL                   string
	MonitorSQL                 string
	CollectorSQL               string
	FactorSQL                  string
	ArchiveSQL                 string
	ArchiveStagingSQL          string
	StrategySQL                string
	TradeSQL                   string
	StorageMetadataSQL         string
	EventsDigest               string
	EventBusTopologyDigest     string
	StoragePebbleLayoutVersion int
	StorageViewIndexVersion    int
	FactorEngineStateVersion   int
	LifecycleStateVersion      int
}

// DefaultInputs 从当前代码内嵌的 schema、事件注册表与版本常量构造期望格式输入。
func DefaultInputs() (Inputs, error) {
	eventsDigest, err := events.MessagesFormatDigest()
	if err != nil {
		return Inputs{}, fmt.Errorf("计算当前事件格式摘要失败：%w", err)
	}
	return Inputs{
		AdminSQL:                   adminschema.AdminSQL(),
		MonitorSQL:                 monitorschema.SQL(),
		CollectorSQL:               collectorschema.AllSQL(),
		FactorSQL:                  factorschema.AllSQL(),
		ArchiveSQL:                 archiveschema.AllSQL(),
		ArchiveStagingSQL:          archiveschema.StagingSQL(),
		StrategySQL:                strategyschema.AllSQL(),
		TradeSQL:                   tradeschema.AllSQL(),
		StorageMetadataSQL:         storageschema.AllSQL(),
		EventsDigest:               eventsDigest,
		EventBusTopologyDigest:     events.TopologyFormatDigest(),
		StoragePebbleLayoutVersion: pkgdataformat.StoragePebbleLayoutVersion,
		StorageViewIndexVersion:    pkgdataformat.StorageViewIndexVersion,
		FactorEngineStateVersion:   pkgdataformat.FactorEngineStateVersion,
		LifecycleStateVersion:      pkgdataformat.LifecycleStateVersion,
	}, nil
}

// Expected 计算当前版本全部 12 个有状态组件的期望数据格式记录。
func Expected() (map[string]pkgdataformat.Record, error) {
	in, err := DefaultInputs()
	if err != nil {
		return nil, err
	}
	return ExpectedWith(in)
}

// ExpectedWith 按指定输入计算全部 12 个有状态组件的期望数据格式记录，口径与各组件启动时落盘完全一致。
func ExpectedWith(in Inputs) (map[string]pkgdataformat.Record, error) {
	lifecycleVersion := strconv.Itoa(in.LifecycleStateVersion)
	out := make(map[string]pkgdataformat.Record, len(statefulComponents))

	addSQLite := func(component, schemaSQL string, withLifecycle bool) error {
		var extra map[string]string
		if withLifecycle {
			extra = map[string]string{pkgdataformat.PartLifecycle: lifecycleVersion}
		}
		rec, err := sqlitedb.FormatRecord(component, schemaSQL, extra)
		if err != nil {
			return fmt.Errorf("计算组件 %s 期望格式失败：%w", component, err)
		}
		out[component] = rec
		return nil
	}

	if err := addSQLite("admin", in.AdminSQL, false); err != nil {
		return nil, err
	}
	if err := addSQLite("monitor", in.MonitorSQL, true); err != nil {
		return nil, err
	}
	if err := addSQLite("collector", in.CollectorSQL, true); err != nil {
		return nil, err
	}
	if err := addSQLite("factor-mgr", in.FactorSQL, false); err != nil {
		return nil, err
	}
	if err := addSQLite("archive-mgr", in.ArchiveSQL, false); err != nil {
		return nil, err
	}
	if err := addSQLite("strategy", in.StrategySQL, true); err != nil {
		return nil, err
	}
	if err := addSQLite("trade", in.TradeSQL, true); err != nil {
		return nil, err
	}

	primaryRec, err := sqlitedb.FormatRecord("storage-primary", in.StorageMetadataSQL, map[string]string{
		pkgdataformat.PartStoragePebbleLayout: strconv.Itoa(in.StoragePebbleLayoutVersion),
		pkgdataformat.PartEvents:              in.EventsDigest,
	})
	if err != nil {
		return nil, fmt.Errorf("计算组件 storage-primary 期望格式失败：%w", err)
	}
	out["storage-primary"] = primaryRec

	viewRec, err := pkgdataformat.Build("storage-view", map[string]string{
		pkgdataformat.PartStorageViewIndex: strconv.Itoa(in.StorageViewIndexVersion),
	})
	if err != nil {
		return nil, fmt.Errorf("计算组件 storage-view 期望格式失败：%w", err)
	}
	out["storage-view"] = viewRec

	eventbusRec, err := pkgdataformat.Build("eventbus", map[string]string{
		pkgdataformat.PartEvents:           in.EventsDigest,
		pkgdataformat.PartEventBusTopology: in.EventBusTopologyDigest,
	})
	if err != nil {
		return nil, fmt.Errorf("计算组件 eventbus 期望格式失败：%w", err)
	}
	out["eventbus"] = eventbusRec

	engineRec, err := pkgdataformat.Build("factor-engine", map[string]string{
		pkgdataformat.PartFactorEngineState: strconv.Itoa(in.FactorEngineStateVersion),
		pkgdataformat.PartLifecycle:         lifecycleVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("计算组件 factor-engine 期望格式失败：%w", err)
	}
	out["factor-engine"] = engineRec

	stagingHash, err := sqlitedb.SchemaHash(in.ArchiveStagingSQL)
	if err != nil {
		return nil, fmt.Errorf("计算组件 archive-worker 暂存表哈希失败：%w", err)
	}
	workerRec, err := pkgdataformat.Build("archive-worker", map[string]string{
		pkgdataformat.PartStagingSchema: stagingHash,
	})
	if err != nil {
		return nil, fmt.Errorf("计算组件 archive-worker 期望格式失败：%w", err)
	}
	out["archive-worker"] = workerRec

	return out, nil
}
