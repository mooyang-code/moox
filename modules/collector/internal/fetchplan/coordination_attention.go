package fetchplan

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
)

// coordinationState 是一个空间的采集计划协调状态。
type coordinationState struct {
	// lastSuccess 是最近一次对账成功的时刻；零值表示本进程还没成功过。
	lastSuccess time.Time
	// failure 是最近一轮协调失败的原因（见 assignmentFailureReasons）；空表示没有失败。
	failure string
	// pending 表示有进行中的协调（未生效的计划或在跑的云端操作）；pendingSince 是它开始的时刻（可能不知道，为零值）。
	pending      bool
	pendingSince time.Time
}

// coordinationStates 是各空间的协调状态，对账协程写、采集指标时读。
type coordinationStates struct {
	mu     sync.Mutex
	spaces map[string]*coordinationState
}

func (s *coordinationStates) update(spaceID string, apply func(*coordinationState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spaces == nil {
		s.spaces = map[string]*coordinationState{}
	}
	state := s.spaces[spaceID]
	if state == nil {
		state = &coordinationState{}
		s.spaces[spaceID] = state
	}
	apply(state)
}

// CoordinationAttention 返回采集时现算的关注项来源 fetch_coordination_failing：
//   - 协调还在进行、且开始不满 pendingGrace：不报（正常的进行中）；
//   - 最近一轮协调失败：按失败原因给出处理办法；
//   - 最近一次对账成功已超过 staleAfter：写明最后对账成功的时刻。
//
// 本进程还没对账成功过、也没失败过的空间不报（刚启动）。detail 只写空间与时刻，不写已过去多久。
func (m *ControlMetrics) CoordinationAttention(kind string, staleAfter, pendingGrace time.Duration) func(now time.Time) map[string][]string {
	return func(now time.Time) map[string][]string {
		if m == nil {
			return nil
		}
		m.coordination.mu.Lock()
		defer m.coordination.mu.Unlock()
		var items []string
		for spaceID, state := range m.coordination.spaces {
			if state.pending && !state.pendingSince.IsZero() && now.Sub(state.pendingSince) >= 0 && now.Sub(state.pendingSince) <= pendingGrace {
				continue
			}
			switch {
			case state.failure != "":
				items = append(items, spaceID+"："+coordinationFailureText(state.failure))
			case !state.lastSuccess.IsZero() && now.Sub(state.lastSuccess) > staleAfter:
				items = append(items, spaceID+"：最近一次采集计划对账已超过允许时间，最后对账成功 "+report.AttentionTime(state.lastSuccess))
			}
		}
		if len(items) == 0 {
			return nil
		}
		sort.Strings(items)
		return map[string][]string{kind: {strings.Join(items, "；")}}
	}
}

// coordinationFailureTexts 是各协调失败原因的告警文案。
var coordinationFailureTexts = map[string]string{
	"quota":       "函数个数、命名空间或预留并发配额超限：用 moox-cli collector function list --space-id <空间> --drift 找出计划外的函数（unplanned 一组），确认后按各行的 delete_command 删除（moox-cli collector function delete --space-id <空间> --name <函数名> --namespace <命名空间> --region <地域>，带上命名空间与地域才不会连同在用的同名函数一起删）；换过云账号的，旧账号下的函数用 moox-cli collector function inspect 查出来到腾讯云控制台手工删",
	"cloud":       "调用腾讯云失败：检查云账号密钥、限频和网络",
	"environment": "函数运行环境生成失败：请检查环境变量大小",
	"rules":       "采集规则读取失败：请检查 Collector 规则配置和存储连接",
	"store":       "Collector 本地数据库读写失败：请检查 Collector 的 SQLite 数据库、磁盘空间和权限",
	"storage":     "标的快照登记失败：请检查 Storage 连接，以及 Collector 与 Storage 计算的快照哈希是否一致",
	"package":     "采集函数代码包解析失败：用 moox-cli collector package list --space-id <空间> 检查代码包是否已上传、状态是否可用",
	"dataset":     "数据集属性（允许迟到）设置失败：请检查 Storage 连接和数据集配置",
	"activity":    "采集计划活动日志未写入，计划保持未生效：请检查 Admin 的活动日志服务",
	"preflight":   "创建新采集计划前的检查未通过：请检查数据源前置条件，例如 A 股交易日历的覆盖期",
	"capacity":    "采集计划无法放进函数：请检查每函数标的数（symbols_per_function）、环境变量大小和外部入口配置",
}

// coordinationFailureText 返回失败原因的告警文案；没有专属文案时给出通用提示。
func coordinationFailureText(reason string) string {
	if text := coordinationFailureTexts[reason]; text != "" {
		return text
	}
	return "采集计划协调失败：请检查 Collector 日志、腾讯云调用和函数网络"
}
