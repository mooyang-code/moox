package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
)

// TaskGroup is one independent timer workload. A function must never mix
// datasets, frequencies, or market types because those values are part of the
// function's static environment.
type TaskGroup struct {
	Provider        string
	MarketType      string
	DatasetID       string
	Frequency       string
	Subjects        []string
	ExternalSymbols map[string]string
}

// NodeAssignment is the deterministic desired state for one SCF node.
type NodeAssignment struct {
	NodeID          string
	FunctionName    string
	Region          string
	Provider        string
	MarketType      string
	DatasetID       string
	Frequency       string
	Subjects        []string
	ExternalSymbols map[string]string
	ProviderChain   []string
	RouteVersion    string
	GroupID         int
	Cron            string
	Enabled         bool
	AssignmentHash  string
}

var stockCNProviderWeights = map[string]int{
	"eastmoney": 1,
	"sina":      1,
	"tencent":   1,
}

// BuildStockCNAssignments maps the published Timer fleet one-to-one to stable
// rendezvous groups. The fleet size is a release decision; universe refreshes
// only change the per-node environment and never create or remove functions.
func BuildStockCNAssignments(group TaskGroup, nodes []scfinvoker.Node, maxSubjects int, tradingDate string) ([]NodeAssignment, error) {
	if maxSubjects <= 0 {
		return nil, fmt.Errorf("max subjects must be positive")
	}
	group.Provider = strings.ToLower(strings.TrimSpace(group.Provider))
	group.MarketType = strings.ToLower(strings.TrimSpace(group.MarketType))
	group.DatasetID = strings.TrimSpace(group.DatasetID)
	group.Frequency = strings.ToLower(strings.TrimSpace(group.Frequency))
	group.Subjects = normalizeSubjects(group.Subjects)
	if group.MarketType != "equity" || group.DatasetID != StockCNDatasetID || group.Frequency != "1m" || len(group.Subjects) == 0 {
		return nil, fmt.Errorf("stock_cn assignment requires equity/%s/1m and at least one subject", StockCNDatasetID)
	}
	if group.ExternalSymbols == nil {
		return nil, fmt.Errorf("task group external symbol mapping is required")
	}
	for _, subject := range group.Subjects {
		if strings.TrimSpace(group.ExternalSymbols[subject]) == "" {
			return nil, fmt.Errorf("subject %s has no external symbol", subject)
		}
	}
	timerNodes := eligibleTimerNodes(nodes)
	if len(timerNodes) == 0 {
		return nil, fmt.Errorf("timer assignment capacity insufficient: stock_cn requires a published Timer SCF fleet")
	}
	sort.Slice(timerNodes, func(i, j int) bool {
		left, right := strings.TrimSpace(timerNodes[i].FunctionName), strings.TrimSpace(timerNodes[j].FunctionName)
		if left == right {
			return timerNodes[i].NodeID < timerNodes[j].NodeID
		}
		return left < right
	})
	groups, err := marketdata.RendezvousAssign(group.Subjects, len(timerNodes), StockCNRouteID)
	if err != nil {
		return nil, fmt.Errorf("assign stock_cn rendezvous groups: %w", err)
	}
	groups, err = rebalanceRendezvousGroups(groups, maxSubjects, StockCNRouteID)
	if err != nil {
		return nil, err
	}
	chains, err := marketdata.AssignProviderChains(len(timerNodes), stockCNProviderWeights, StockCNRouteID, strings.TrimSpace(tradingDate))
	if err != nil {
		return nil, fmt.Errorf("assign stock_cn provider chains: %w", err)
	}
	assignments := make([]NodeAssignment, 0, len(timerNodes))
	for groupID, subjects := range groups {
		if len(subjects) > maxSubjects {
			return nil, fmt.Errorf("stock_cn rendezvous group %d contains %d subjects; maximum is %d", groupID, len(subjects), maxSubjects)
		}
		externals := make(map[string]string, len(subjects))
		hashParts := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			external := strings.TrimSpace(group.ExternalSymbols[subject])
			externals[subject] = external
			hashParts = append(hashParts, subject+"="+external)
		}
		node := timerNodes[groupID]
		chain := append([]string(nil), chains[groupID]...)
		assignments = append(assignments, NodeAssignment{
			NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region,
			Provider: chain[0], MarketType: group.MarketType, DatasetID: group.DatasetID, Frequency: group.Frequency,
			Subjects: append([]string(nil), subjects...), ExternalSymbols: externals, ProviderChain: chain,
			RouteVersion: StockCNRouteID, GroupID: groupID, Cron: stockCNStaggeredCron(groupID), Enabled: len(subjects) > 0,
			AssignmentHash: AssignmentHash(chain[0], strings.Join(chain, "|"), StockCNRouteID, strconv.Itoa(groupID), group.MarketType, group.DatasetID, group.Frequency, strings.Join(hashParts, "|")),
		})
	}
	return assignments, nil
}

func stockCNStaggeredCron(groupID int) string {
	second := groupID % 45
	if second < 0 {
		second += 45
	}
	return fmt.Sprintf("%d * * * * * *", second)
}

func rebalanceRendezvousGroups(groups [][]string, maxSubjects int, routeVersion string) ([][]string, error) {
	if len(groups) == 0 || maxSubjects <= 0 {
		return nil, fmt.Errorf("stock_cn rendezvous capacity must be positive")
	}
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	if total > len(groups)*maxSubjects {
		return nil, fmt.Errorf("stock_cn timer assignment capacity insufficient: %d subjects exceed %d groups x %d subjects", total, len(groups), maxSubjects)
	}
	result := make([][]string, len(groups))
	overflow := make([]string, 0)
	for groupID, subjects := range groups {
		keep := len(subjects)
		if keep > maxSubjects {
			keep = maxSubjects
			overflow = append(overflow, subjects[keep:]...)
		}
		result[groupID] = append([]string(nil), subjects[:keep]...)
	}
	sort.Strings(overflow)
	for _, subject := range overflow {
		candidates := make([]int, 0, len(result))
		for groupID := range result {
			if len(result[groupID]) < maxSubjects {
				candidates = append(candidates, groupID)
			}
		}
		choice, err := marketdata.RendezvousAssign([]string{subject}, len(candidates), routeVersion+"\x00overflow")
		if err != nil {
			return nil, fmt.Errorf("rebalance stock_cn rendezvous overflow: %w", err)
		}
		selected := -1
		for candidateIndex, assigned := range choice {
			if len(assigned) > 0 {
				selected = candidates[candidateIndex]
				break
			}
		}
		if selected < 0 {
			return nil, fmt.Errorf("rebalance stock_cn rendezvous overflow subject %s: no candidate group", subject)
		}
		result[selected] = append(result[selected], subject)
	}
	for groupID := range result {
		sort.Strings(result[groupID])
	}
	return result, nil
}

// BuildAssignments sorts both inputs before assigning shards, so a refresh
// does not churn functions merely because Storage returned a different order.
func BuildAssignments(groups []TaskGroup, nodes []scfinvoker.Node, maxSubjects int) ([]NodeAssignment, error) {
	if maxSubjects <= 0 {
		return nil, fmt.Errorf("max subjects must be positive")
	}
	timerNodes := eligibleTimerNodes(nodes)
	timerNodes = roundRobinRegions(timerNodes)
	normalized := make([]TaskGroup, 0, len(groups))
	for _, group := range groups {
		group.Provider = strings.ToLower(strings.TrimSpace(group.Provider))
		group.MarketType = strings.ToLower(strings.TrimSpace(group.MarketType))
		group.DatasetID = strings.TrimSpace(group.DatasetID)
		group.Frequency = strings.TrimSpace(group.Frequency)
		cron, err := CronForFrequency(group.Frequency)
		if err != nil {
			return nil, err
		}
		_ = cron
		group.Subjects = normalizeSubjects(group.Subjects)
		if group.Provider == "" || group.MarketType == "" || group.DatasetID == "" || len(group.Subjects) == 0 {
			return nil, fmt.Errorf("task group has incomplete identity or no subjects")
		}
		if group.ExternalSymbols == nil {
			return nil, fmt.Errorf("task group external symbol mapping is required")
		}
		for _, subject := range group.Subjects {
			if strings.TrimSpace(group.ExternalSymbols[subject]) == "" {
				return nil, fmt.Errorf("subject %s has no external symbol", subject)
			}
		}
		normalized = append(normalized, group)
	}
	sort.Slice(normalized, func(i, j int) bool { return groupKey(normalized[i]) < groupKey(normalized[j]) })
	needed := 0
	for _, group := range normalized {
		needed += (len(group.Subjects) + maxSubjects - 1) / maxSubjects
	}
	if needed > len(timerNodes) {
		return nil, fmt.Errorf("timer assignment capacity insufficient: %d Timer nodes are required for the configured dataset/frequency shards, but only %d are available; increase the Timer SCF fleet", needed, len(timerNodes))
	}
	assignments := make([]NodeAssignment, 0, len(timerNodes))
	nodeIndex := 0
	for _, group := range normalized {
		cron, _ := CronForFrequency(group.Frequency)
		for start := 0; start < len(group.Subjects); start += maxSubjects {
			end := start + maxSubjects
			if end > len(group.Subjects) {
				end = len(group.Subjects)
			}
			subjects := append([]string(nil), group.Subjects[start:end]...)
			hashParts := make([]string, 0, len(subjects))
			externals := make(map[string]string, len(subjects))
			for _, subject := range subjects {
				external := strings.TrimSpace(group.ExternalSymbols[subject])
				externals[subject] = external
				hashParts = append(hashParts, subject+"="+external)
			}
			node := timerNodes[nodeIndex]
			nodeIndex++
			assignments = append(assignments, NodeAssignment{NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region, Provider: group.Provider, MarketType: group.MarketType, DatasetID: group.DatasetID, Frequency: group.Frequency, Subjects: subjects, ExternalSymbols: externals, Cron: cron, Enabled: true, AssignmentHash: AssignmentHash(group.Provider, group.MarketType, group.DatasetID, group.Frequency, strings.Join(hashParts, "|"))})
		}
	}
	for ; nodeIndex < len(timerNodes); nodeIndex++ {
		node := timerNodes[nodeIndex]
		assignments = append(assignments, NodeAssignment{NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region, Enabled: false, AssignmentHash: AssignmentHash()})
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].NodeID < assignments[j].NodeID })
	return assignments, nil
}

func eligibleTimerNodes(nodes []scfinvoker.Node) []scfinvoker.Node {
	timerNodes := make([]scfinvoker.Node, 0, len(nodes))
	for _, node := range nodes {
		if strings.EqualFold(strings.TrimSpace(node.NodeType), "scf-event") && strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") {
			timerNodes = append(timerNodes, node)
		}
	}
	return timerNodes
}

func roundRobinRegions(nodes []scfinvoker.Node) []scfinvoker.Node {
	byRegion := make(map[string][]scfinvoker.Node)
	regions := make([]string, 0)
	for _, node := range nodes {
		region := strings.TrimSpace(node.Region)
		if _, ok := byRegion[region]; !ok {
			regions = append(regions, region)
		}
		byRegion[region] = append(byRegion[region], node)
	}
	sort.Strings(regions)
	for _, region := range regions {
		sort.Slice(byRegion[region], func(i, j int) bool { return byRegion[region][i].NodeID < byRegion[region][j].NodeID })
	}
	result := make([]scfinvoker.Node, 0, len(nodes))
	for index := 0; len(result) < len(nodes); index++ {
		for _, region := range regions {
			if index < len(byRegion[region]) {
				result = append(result, byRegion[region][index])
			}
		}
	}
	return result
}

func normalizeSubjects(subjects []string) []string {
	seen := make(map[string]struct{}, len(subjects))
	result := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		subject = strings.ToUpper(strings.TrimSpace(subject))
		if subject == "" {
			continue
		}
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		result = append(result, subject)
	}
	sort.Strings(result)
	return result
}

func groupKey(group TaskGroup) string {
	return strings.Join([]string{group.Provider, group.MarketType, group.DatasetID, group.Frequency}, "\x00")
}

// AssignmentHash intentionally excludes timestamps so unchanged assignments
// do not cause an UpdateFunctionConfiguration call every reconciliation tick.
func AssignmentHash(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])[:16]
}

func CronForFrequency(frequency string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(frequency)) {
	case "1m":
		return "0 * * * * * *", nil
	case "5m":
		return "0 */5 * * * * *", nil
	case "15m":
		return "0 */15 * * * * *", nil
	case "30m":
		return "0 */30 * * * * *", nil
	case "1h":
		return "0 0 * * * * *", nil
	case "4h":
		return "0 0 */4 * * * *", nil
	case "1d":
		return "0 0 0 * * * *", nil
	default:
		return "", fmt.Errorf("unsupported timer frequency %q", frequency)
	}
}
