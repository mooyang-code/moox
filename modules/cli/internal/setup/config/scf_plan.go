package config

// SCFCloudAccount 返回 SCF 使用的云账号（[scf_plan.cloud_account]）。
func (m Manifest) SCFCloudAccount() SCFCloudAccountConfig { return m.SCFPlan.CloudAccount }

// SCFEnabled 表示是否启用 SCF 采集：云账号登记与日志服务开通共用。
func (m Manifest) SCFEnabled() bool { return m.SCFPlan.Enabled }

// validateSCFPlan 规整并校验 [scf_plan]，规则在新包里，结果写回旧结构。
func validateSCFPlan(manifest *Manifest) error {
	cfg := manifest.TopologyConfig()
	err := cfg.CheckSCFPlan()
	manifest.SCFPlan = cfg.SCFPlan
	return err
}

// SCFPlanAccess 返回某地域 SCF 函数访问 Access 的私网入口。
func (m Manifest) SCFPlanAccess(region string) (address, id string, err error) {
	return m.TopologyConfig().SCFPlanAccess(region)
}

// SCFPlanRegionNames 返回计划用到的地域（排序）。
func (m Manifest) SCFPlanRegionNames() []string { return m.TopologyConfig().SCFPlanRegionNames() }
