package dataformat

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	pkgdataformat "github.com/mooyang-code/moox/packages/dataformat"
)

// 单个有状态组件的格式比对结果。
const (
	// StatusNoData 表示组件尚无本地数据文件，可以直接保留继续部署。
	StatusNoData = "no_data"
	// StatusMatch 表示已有本地数据且 data-format 与新版本一致，可以保留数据升级。
	StatusMatch = "match"
	// StatusUnknown 表示已有本地数据但读不到或解析不了 data-format，按格式未知拒绝保留。
	StatusUnknown = "unknown"
	// StatusChanged 表示已有本地数据且 data-format 与新版本不同，拒绝保留。
	StatusChanged = "changed"
)

// ComponentCheck 是单台主机上单个有状态组件的格式比对结论。
type ComponentCheck struct {
	Host         string   `json:"host"`
	Component    string   `json:"component"`
	Status       string   `json:"status"`
	Expected     string   `json:"expected,omitempty"`
	Actual       string   `json:"actual,omitempty"`
	ChangedParts []string `json:"changed_parts,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// Report 是一次预检的全部格式比对结果。
type Report struct {
	CanKeep      bool             `json:"can_keep"`
	Checks       []ComponentCheck `json:"checks"`
	Incompatible []ComponentCheck `json:"incompatible,omitempty"`
}

// HostScope 描述单台待检主机上的目标组件集合与只读探测结果。
type HostScope struct {
	Host        string
	Components  []string
	DataFormats map[string]deployscan.ComponentDataFormat
}

// CheckHost 按给定 expected 期望格式，比对单台主机上 components 的本地数据状态。
// 无状态组件（host-gateway、host-agent、egress-proxy、collector-stream）不参与比对并跳过。
func CheckHost(host string, components []string, observed map[string]deployscan.ComponentDataFormat, expected map[string]pkgdataformat.Record) Report {
	return CheckHosts([]HostScope{{
		Host:        host,
		Components:  components,
		DataFormats: observed,
	}}, expected)
}

// CheckHosts 按给定 expected 期望格式，依次比对多台主机上的有状态组件。
func CheckHosts(scopes []HostScope, expected map[string]pkgdataformat.Record) Report {
	report := Report{
		CanKeep: true,
		Checks:  []ComponentCheck{},
	}
	for _, scope := range scopes {
		seen := make(map[string]struct{}, len(scope.Components))
		for _, component := range scope.Components {
			if _, dup := seen[component]; dup {
				continue
			}
			seen[component] = struct{}{}
			want, ok := expected[component]
			if !ok {
				continue
			}
			check := checkOne(scope.Host, component, scope.DataFormats[component], want)
			report.Checks = append(report.Checks, check)
			if check.Status == StatusUnknown || check.Status == StatusChanged {
				report.Incompatible = append(report.Incompatible, check)
				report.CanKeep = false
			}
		}
	}
	return report
}

func checkOne(host, component string, obs deployscan.ComponentDataFormat, want pkgdataformat.Record) ComponentCheck {
	check := ComponentCheck{
		Host:      host,
		Component: component,
		Expected:  want.Format,
	}
	if !obs.HasData {
		check.Status = StatusNoData
		check.Reason = "尚无本地数据，可以保留"
		return check
	}
	rawText := strings.TrimSpace(obs.FormatText)
	if rawText == "" {
		check.Status = StatusUnknown
		check.Reason = "已有本地数据但缺少 data-format 文件，格式未知，不能保留"
		return check
	}
	got, err := pkgdataformat.Parse([]byte(rawText))
	if err != nil {
		check.Status = StatusUnknown
		check.Reason = fmt.Sprintf("data-format 文件损坏或无效（%v），格式未知，不能保留", err)
		return check
	}
	if got.Component != component {
		check.Status = StatusUnknown
		check.Reason = fmt.Sprintf("data-format 记录的组件为 %s 而非 %s，格式未知，不能保留", got.Component, component)
		return check
	}
	check.Actual = got.Format
	if got.Format == want.Format {
		check.Status = StatusMatch
		check.Reason = "数据格式与当前版本一致，可以保留"
		return check
	}
	diff := pkgdataformat.DiffParts(want, got)
	check.Status = StatusChanged
	check.ChangedParts = diff
	check.Reason = fmt.Sprintf("数据格式已变化（变更项：%s），不能保留", strings.Join(diff, "、"))
	return check
}

// CheckHostNow 使用当前代码的期望格式比对单台主机上指定的组件集合。
func CheckHostNow(host string, components []string, observed map[string]deployscan.ComponentDataFormat) (Report, error) {
	expected, err := Expected()
	if err != nil {
		return Report{}, err
	}
	return CheckHost(host, components, observed, expected), nil
}

// CheckTopologyReports 按拓扑声明的各主机组件列表，用当前代码期望格式比对整份 deployscan.Scan 结果（供 setup install 预检调用）。
func CheckTopologyReports(cfg *topology.Config, reports []deployscan.HostReport) (Report, error) {
	if cfg == nil {
		return Report{}, fmt.Errorf("数据格式预检需要拓扑配置")
	}
	expected, err := Expected()
	if err != nil {
		return Report{}, err
	}
	return CheckTopologyReportsWith(cfg, reports, expected)
}

// CheckTopologyReportsWith 使用指定的 expected 格式表比对拓扑扫描结果。
func CheckTopologyReportsWith(cfg *topology.Config, reports []deployscan.HostReport, expected map[string]pkgdataformat.Record) (Report, error) {
	if cfg == nil {
		return Report{}, fmt.Errorf("数据格式预检需要拓扑配置")
	}
	scopes := make([]HostScope, 0, len(reports))
	for _, rep := range reports {
		components, err := cfg.HostComponents(rep.Host)
		if err != nil {
			return Report{}, err
		}
		scopes = append(scopes, HostScope{
			Host:        rep.Host,
			Components:  components,
			DataFormats: rep.DataFormats,
		})
	}
	return CheckHosts(scopes, expected), nil
}

// IncompatibleError 在存在不能保留数据的组件时返回可读的中文错误；全部可保留时返回 nil。
func IncompatibleError(report Report) error {
	if report.CanKeep || len(report.Incompatible) == 0 {
		return nil
	}
	items := make([]string, 0, len(report.Incompatible))
	for _, item := range report.Incompatible {
		target := item.Component
		if item.Host != "" {
			target = item.Host + "/" + item.Component
		}
		items = append(items, fmt.Sprintf("%s（%s）", target, item.Reason))
	}
	return fmt.Errorf("data_format_incompatible: 以下组件数据格式不兼容，不能保留数据部署：%s。请改走全部覆盖（moox-cli setup install --space-id <空间> --wipe <预检摘要> --yes，清空前会自动备份密钥与告警渠道）", strings.Join(items, "；"))
}
