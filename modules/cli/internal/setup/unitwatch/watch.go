// Package unitwatch 是主机级守护：由 crontab 每分钟调用一次 `moox-runtime watch`，遍历本机部署根下全部单元，
// 按依赖顺序对每个组件执行运行助手的 healthcheck（拉起停止的组件、重启存活检查失败的组件），
// 给出结构化结果，维护持续崩溃的退避状态与台账。设计见 docs/计划/主机守护与告警高可用执行计划.md 第 4.1 节。
package unitwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
)

// 结果分类。
const (
	OutcomeHealthy           = "healthy"            // 进程在、存活检查通过且就绪
	OutcomeNotReady          = "not_ready"          // 进程在、存活检查通过，但 /readyz 未就绪（不重启）
	OutcomeStarted           = "started"            // 原本没在运行，已拉起
	OutcomeRestarted         = "restarted"          // 存活检查失败，已停止并重新拉起
	OutcomeStartFailed       = "start_failed"       // 拉起或重启后没有就绪
	OutcomeCheckFailed       = "check_failed"       // 检查本身出错（环境文件、记录读取等）
	OutcomeIdentityError     = "identity_error"     // 进程身份与记录不符，运行助手拒绝操作，需要人工处理
	OutcomeSkippedPaused     = "skipped_paused"     // 用户暂停的组件
	OutcomeSkippedDraining   = "skipped_draining"   // 停止过程被中断，需要显式 stop 接续
	OutcomeSkippedLocked     = "skipped_locked"     // 维护锁被占用（发布、重启进行中），本轮其余组件都跳过
	OutcomeSkippedDependency = "skipped_dependency" // 上游组件本轮没有运行起来，下一轮再试
	OutcomeSkippedBackoff    = "skipped_backoff"    // 持续崩溃中，未到下一次尝试时间
	OutcomeSkippedDeadline   = "skipped_deadline"   // 本轮已超过软期限，不再开始新的检查
	OutcomeWouldStart        = "would_start"        // --dry-run：组件没在运行，正式运行时会拉起
)

// headComponents 是跨单元的启动依赖顺序，与 bootstrap 的恢复顺序一致（unitbootstrap.startStages）：
// 主机网关要等 Admin 完成同步才就绪，HostAgent 要 EventBus。它们之一没有运行时，其余组件本轮跳过。
var headComponents = []string{"admin", "host-gateway", "eventbus", "host-agent"}

// DefaultSoftDeadline 之后不再开始新的组件检查；已经开始的启停按运行助手自己的预算执行完，
// 不用硬超时取消（取消 stop 会留下 draining 标记，反而让组件永远起不来）。
const DefaultSoftDeadline = 4 * time.Minute

// ExecuteFunc 与 unitruntime.Execute 的签名相同，测试可替换。
type ExecuteFunc func(ctx context.Context, planPath, operation string, ids []string, opts unitruntime.Options) (unitruntime.Result, error)

type Options struct {
	Root         string
	DryRun       bool
	SoftDeadline time.Duration
	Now          func() time.Time
	Execute      ExecuteFunc
	// LoadPlan 读取单元的运行计划，默认 unitruntime.LoadPlan；测试可替换。
	LoadPlan func(path string) (unitruntime.Plan, error)
	// Policy 是持续崩溃与未就绪的参数，零值使用默认值；主机策略缓存里的守护参数会覆盖它。
	Policy Policy
	// PolicyPath 是主机策略缓存，默认 <Root>/run/host-policy.json。
	PolicyPath string
	// NewSender 按渠道创建告警发送器，默认 notification.NewSender；测试可替换。
	NewSender func(notification.ChannelConfig) (notification.Sender, error)
	// PeerProbe 是 control 交叉检查的 TLS 握手，默认 probeTLS；测试可替换。
	PeerProbe PeerProbe
}

// ComponentReport 是一个组件本轮的结果。
type ComponentReport struct {
	Unit       string `json:"unit"`
	Component  string `json:"component"`
	Outcome    string `json:"outcome"`
	PID        int    `json:"pid,omitempty"`
	Ready      bool   `json:"ready"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	// Evidence 是拉起前运行输出日志里最后几条 panic / fatal 行（崩溃线索）。
	Evidence []string `json:"evidence,omitempty"`
	// RecentRestarts 是近 1 小时内自动拉起（含失败）的次数，包括本次。
	RecentRestarts   int   `json:"recent_restarts,omitempty"`
	CrashLoop        bool  `json:"crash_loop,omitempty"`
	EnteredCrashLoop bool  `json:"entered_crash_loop,omitempty"`
	Recovered        bool  `json:"recovered,omitempty"`
	NotReadySeconds  int64 `json:"not_ready_seconds,omitempty"`
}

// Report 是一轮守护的汇总，cron 把它作为一行 JSON 写进 run/watchdog.log。
type Report struct {
	HostID    string    `json:"host_id"`
	StartedAt time.Time `json:"started_at"`
	ElapsedMS int64     `json:"elapsed_ms"`
	DryRun    bool      `json:"dry_run,omitempty"`
	// Barrier 是让整轮跳过的持久屏障："installation" 或 "bootstrap"（发布或初始化被中断，需要按恢复流程处理）。
	Barrier       string                     `json:"barrier,omitempty"`
	BootRecovered []unitruntime.BootRecovery `json:"boot_recovered,omitempty"`
	Components    []ComponentReport          `json:"components"`
	// PeerControl 是非 control 主机对 control 网关的交叉检查结果。
	PeerControl *PeerReport `json:"peer_control,omitempty"`
	// WatchdogDisabled 表示中心策略关闭了守护：本轮只检查，不拉起、不重启、不告警。
	WatchdogDisabled bool `json:"watchdog_disabled,omitempty"`
	// Alerts 是本轮发出的告警标题；AlertErrors 是策略读取或推送失败的说明。
	Alerts      []string `json:"alerts,omitempty"`
	AlertErrors []string `json:"alert_errors,omitempty"`
}

type target struct {
	unit, plan, component string
}

// Run 执行一轮守护。单个组件出错不影响其他组件，只有本机布局本身无法读取时返回错误。
func Run(ctx context.Context, opts Options) (report Report, _ error) {
	opts = opts.withDefaults()
	started := opts.Now()
	report = Report{StartedAt: started.UTC(), DryRun: opts.DryRun, Components: []ComponentReport{}}
	// 具名返回值：延迟赋值要作用在返回给调用方的报告上。
	defer func() { report.ElapsedMS = opts.Now().Sub(started).Milliseconds() }()

	targets, hostID, err := discover(opts.Root, opts.LoadPlan)
	if err != nil {
		return report, err
	}
	report.HostID = hostID
	runDir := filepath.Join(opts.Root, "run")
	policy, _, policyErr := loadPolicy(opts.PolicyPath)
	if policyErr != nil {
		report.AlertErrors = append(report.AlertErrors, "主机策略缓存不可用："+policyErr.Error())
	}
	opts.Policy = applyWatchdog(opts.Policy, policy.Watchdog)
	if policy.Watchdog.Disabled && !opts.DryRun {
		opts.DryRun, report.DryRun, report.WatchdogDisabled = true, true, true
	}
	store, err := openStore(runDir)
	if err != nil {
		return report, err
	}
	state, err := store.load()
	if err != nil {
		return report, err
	}
	// control 交叉检查与本机组件无关，安装屏障存在时也照常进行。
	var peerMessages []notification.Message
	peer, peerErr := checkControlPeer(ctx, opts.Root, opts.PeerProbe)
	if peerErr != nil {
		report.AlertErrors = append(report.AlertErrors, "control 交叉检查不可用："+peerErr.Error())
	}
	if peer != nil {
		report.PeerControl = peer
		if !opts.DryRun {
			ps, err := store.loadPeer()
			if err != nil {
				return report, err
			}
			peerMessages = observePeer(peer, ps, hostID, opts.Now())
			if err := store.savePeer(ps); err != nil {
				return report, err
			}
		}
	}
	for _, barrier := range []string{"installation", "bootstrap"} {
		if _, err := os.Lstat(filepath.Join(runDir, barrier+".json")); err == nil {
			report.Barrier = barrier
			if !opts.DryRun {
				opts.deliver(ctx, &report, state, policy, peerMessages)
				if err := store.save(state); err != nil {
					return report, err
				}
			}
			return report, nil
		} else if !os.IsNotExist(err) {
			return report, err
		}
	}

	dependencyDown := ""
	locked := false
	for _, t := range targets {
		entry := checked{ComponentReport: ComponentReport{Unit: t.unit, Component: t.component}}
		head := slices.Contains(headComponents, t.component)
		cs := state.component(t.component)
		switch {
		case locked:
			entry.Outcome = OutcomeSkippedLocked
		case opts.Now().Sub(started) >= opts.SoftDeadline:
			entry.Outcome = OutcomeSkippedDeadline
		case dependencyDown != "" && !head:
			entry.Outcome, entry.Error = OutcomeSkippedDependency, dependencyDown+" 没有运行"
		case !opts.DryRun && cs.backoffPending(opts.Now(), opts.Policy):
			entry.Outcome, entry.CrashLoop = OutcomeSkippedBackoff, true
		default:
			checkStarted := opts.Now()
			entry = check(ctx, opts, t, entry.ComponentReport)
			entry.DurationMS = opts.Now().Sub(checkStarted).Milliseconds()
		}
		if entry.Outcome == OutcomeSkippedLocked {
			locked = true
		}
		if head && entry.Outcome != OutcomeSkippedLocked && entry.Outcome != OutcomeSkippedDeadline && !running(entry.Outcome) && dependencyDown == "" {
			dependencyDown = t.component
		}
		if !opts.DryRun {
			if entry.Outcome == OutcomeStarted || entry.Outcome == OutcomeRestarted {
				entry.Evidence = crashEvidence(filepath.Join(runDir, "logs", t.component+".log"))
			}
			cs.observe(opts.Now(), &entry.ComponentReport, opts.Policy)
			if err := store.appendLedger(t.component, opts.Now(), entry.ComponentReport, cs); err != nil {
				entry.Error = strings.TrimSpace(entry.Error + "；台账写入失败：" + err.Error())
			}
		}
		report.BootRecovered = append(report.BootRecovered, entry.bootRecovered...)
		report.Components = append(report.Components, entry.ComponentReport)
	}
	sort.SliceStable(report.BootRecovered, func(i, j int) bool { return report.BootRecovered[i].Component < report.BootRecovered[j].Component })
	if !opts.DryRun {
		opts.deliver(ctx, &report, state, policy, peerMessages)
		if err := store.save(state); err != nil {
			return report, err
		}
	}
	return report, nil
}

// deliver 决定并发出本轮告警，把结果写进报告。
func (o Options) deliver(ctx context.Context, report *Report, state *watchState, policy hostpolicy.Policy, extra []notification.Message) {
	messages := append(planAlerts(*report, state, o.Now(), o.Policy), extra...)
	for _, message := range messages {
		report.Alerts = append(report.Alerts, message.Title)
	}
	report.AlertErrors = append(report.AlertErrors, sendAlerts(ctx, messages, policy, o.NewSender)...)
}

// running 报告结果是否表示组件此刻在运行（依赖判断用）。
func running(outcome string) bool {
	return outcome == OutcomeHealthy || outcome == OutcomeNotReady || outcome == OutcomeStarted || outcome == OutcomeRestarted
}

type checked struct {
	ComponentReport
	bootRecovered []unitruntime.BootRecovery
}

func check(ctx context.Context, opts Options, t target, base ComponentReport) checked {
	entry := checked{ComponentReport: base}
	operation := "healthcheck"
	if opts.DryRun {
		operation = "status"
	}
	result, err := opts.Execute(ctx, t.plan, operation, []string{t.component}, unitruntime.Options{})
	entry.bootRecovered = result.BootRecovered
	if result.Skipped {
		entry.Outcome = OutcomeSkippedLocked
		return entry
	}
	var status unitruntime.Status
	if len(result.Components) > 0 {
		status = result.Components[0]
	}
	entry.PID, entry.Ready = status.PID, status.Ready
	if err != nil {
		entry.Error = err.Error()
		switch {
		case unitruntime.IsIdentityError(err):
			entry.Outcome = OutcomeIdentityError
		case status.Action != "":
			entry.Outcome = OutcomeStartFailed
		default:
			entry.Outcome = OutcomeCheckFailed
		}
		return entry
	}
	switch {
	case status.State == "paused":
		entry.Outcome = OutcomeSkippedPaused
	case status.State == "draining":
		entry.Outcome = OutcomeSkippedDraining
	case opts.DryRun && status.State == "stopped":
		entry.Outcome = OutcomeWouldStart
	case status.Action == "started":
		entry.Outcome = OutcomeStarted
	case status.Action == "restarted":
		entry.Outcome = OutcomeRestarted
	case status.State == "running" && !status.Ready:
		entry.Outcome = OutcomeNotReady
	case status.State == "running":
		entry.Outcome = OutcomeHealthy
	default:
		entry.Outcome, entry.Error = OutcomeCheckFailed, fmt.Sprintf("unexpected state %q", status.State)
	}
	return entry
}

// discover 找出 <root>/*/current/runtime.json，按依赖顺序排出待检查的组件：先 headComponents，再按单元名、计划顺序排其余组件。
// 名字带“.”的单元目录（旧部署留下的 .next / .previous / .failed）跳过。
func discover(root string, loadPlan func(string) (unitruntime.Plan, error)) ([]target, string, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "current", "runtime.json"))
	if err != nil {
		return nil, "", err
	}
	sort.Strings(paths)
	var heads, rest []target
	hostID := ""
	for _, path := range paths {
		unit := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if strings.Contains(unit, ".") {
			continue
		}
		plan, err := loadPlan(path)
		if err != nil {
			return nil, "", fmt.Errorf("unit %s: %w", unit, err)
		}
		if hostID != "" && plan.HostID != hostID {
			return nil, "", fmt.Errorf("unit %s belongs to host %s, not %s", unit, plan.HostID, hostID)
		}
		hostID = plan.HostID
		for _, component := range plan.Components {
			t := target{unit: unit, plan: path, component: component.ID}
			if slices.Contains(headComponents, component.ID) {
				heads = append(heads, t)
			} else {
				rest = append(rest, t)
			}
		}
	}
	if hostID == "" {
		return nil, "", errors.New("no installed units under " + root)
	}
	sort.SliceStable(heads, func(i, j int) bool {
		return slices.Index(headComponents, heads[i].component) < slices.Index(headComponents, heads[j].component)
	})
	return append(heads, rest...), hostID, nil
}

func (o Options) withDefaults() Options {
	if o.SoftDeadline <= 0 {
		o.SoftDeadline = DefaultSoftDeadline
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Execute == nil {
		o.Execute = unitruntime.Execute
	}
	if o.LoadPlan == nil {
		o.LoadPlan = unitruntime.LoadPlan
	}
	if o.PolicyPath == "" {
		o.PolicyPath = hostpolicy.Path(o.Root)
	}
	if o.NewSender == nil {
		o.NewSender = notification.NewSender
	}
	if o.PeerProbe == nil {
		o.PeerProbe = probeTLS
	}
	o.Policy = o.Policy.withDefaults()
	return o
}
