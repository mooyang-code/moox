package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
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
	MarketID        string
	InstrumentType  string
	SourceID        string
	SeriesTag       string
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
	RouteProvider   string
	MarketType      string
	MarketID        string
	InstrumentType  string
	SourceID        string
	SeriesTag       string
	DatasetID       string
	Frequency       string
	Subjects        []string
	ExternalSymbols map[string]string
	ProviderChain   []string
	RouteVersion    string
	GroupID         int
	GroupCount      int
	Cron            string
	Enabled         bool
	AssignmentHash  string
}

// StockCNStaggerConfig is the release-time Timer fan-out contract. A fixed
// second window keeps the number of simultaneous provider requests bounded
// without creating or deleting functions when the active instrument set
// changes.
type StockCNStaggerConfig struct {
	StartSecond        int
	WindowSeconds      int
	MaxStartsPerSecond int
}

func DefaultStockCNStaggerConfig() StockCNStaggerConfig {
	return StockCNStaggerConfig{StartSecond: 5, WindowSeconds: 35, MaxStartsPerSecond: 6}
}

func (c StockCNStaggerConfig) Validate(timerFunctionCount int) error {
	if timerFunctionCount <= 0 {
		return fmt.Errorf("stock_cn timer function count must be positive")
	}
	if c.StartSecond < 0 || c.StartSecond > 59 {
		return fmt.Errorf("stock_cn stagger start second must be between 0 and 59")
	}
	if c.WindowSeconds <= 0 || c.WindowSeconds > 60 || c.StartSecond+c.WindowSeconds > 60 {
		return fmt.Errorf("stock_cn stagger window must fit between second %d and 59", c.StartSecond)
	}
	if c.MaxStartsPerSecond <= 0 {
		return fmt.Errorf("stock_cn max starts per second must be positive")
	}
	startsPerSecond := (timerFunctionCount + c.WindowSeconds - 1) / c.WindowSeconds
	if startsPerSecond > c.MaxStartsPerSecond {
		return fmt.Errorf("stock_cn stagger requires up to %d starts per second, above configured maximum %d", startsPerSecond, c.MaxStartsPerSecond)
	}
	return nil
}

func stockCNAssignmentRoute() (string, []stockCNSource, error) {
	route, err := loadStockCNRoute()
	if err != nil {
		return "", nil, fmt.Errorf("load stock_cn assignment route: %w", err)
	}
	sources := route.KlinePrimarySources()
	if len(sources) < 2 {
		return "", nil, fmt.Errorf("stock_cn assignment route must have at least two primary kline providers")
	}
	return route.RouteID, sources, nil
}

// BuildStockCNAssignments maps the published Timer fleet one-to-one to stable
// rendezvous groups. The fleet size and measured group-size safety limit are
// release configuration; neither is inferred from the nodes visible today.
func BuildStockCNAssignments(group TaskGroup, nodes []scfinvoker.Node, measuredSafeGroupSize int, tradingDate string, expectedCounts ...int) ([]NodeAssignment, error) {
	return BuildStockCNAssignmentsWithStagger(group, nodes, measuredSafeGroupSize, tradingDate, DefaultStockCNStaggerConfig(), expectedCounts...)
}

// BuildStockCNAssignmentsWithStagger is the configurable form used by the
// Collector reconciler. The legacy wrapper above keeps direct callers on the
// conservative default while production receives the rendered release value.
func BuildStockCNAssignmentsWithStagger(group TaskGroup, nodes []scfinvoker.Node, measuredSafeGroupSize int, tradingDate string, stagger StockCNStaggerConfig, expectedCounts ...int) ([]NodeAssignment, error) {
	if measuredSafeGroupSize <= 0 {
		return nil, fmt.Errorf("stock_cn measured safe group size must be positive")
	}
	if len(expectedCounts) != 1 || expectedCounts[0] <= 0 {
		return nil, fmt.Errorf("stock_cn requires an explicit positive timer function count")
	}
	group.Provider = strings.ToLower(strings.TrimSpace(group.Provider))
	group.MarketType = strings.ToLower(strings.TrimSpace(group.MarketType))
	group.DatasetID = strings.TrimSpace(group.DatasetID)
	group.Frequency = strings.ToLower(strings.TrimSpace(group.Frequency))
	group.Subjects = normalizeSubjects(group.Subjects)
	if group.MarketType != "equity" || group.DatasetID != StockCNDatasetID || group.Frequency != "1m" {
		return nil, fmt.Errorf("stock_cn assignment requires equity/%s/1m", StockCNDatasetID)
	}
	eligible := eligibleTimerNodes(nodes)
	expectedCount := expectedCounts[0]
	if stagger == (StockCNStaggerConfig{}) {
		stagger = DefaultStockCNStaggerConfig()
	}
	if err := stagger.Validate(expectedCount); err != nil {
		return nil, err
	}
	timerNodes, err := orderedStockCNTimerNodes(eligible, expectedCount)
	if err != nil {
		return nil, err
	}
	if len(timerNodes) == 0 {
		return nil, fmt.Errorf("timer assignment capacity insufficient: stock_cn requires a published Timer SCF fleet")
	}
	routeVersion, sources, err := stockCNAssignmentRoute()
	if err != nil {
		return nil, err
	}
	if len(group.Subjects) == 0 {
		return disabledStockCNAssignments(group, timerNodes, routeVersion, sources, stagger)
	}
	sourceGroups, err := assignStockCNSourceGroups(group.Subjects, sources, len(timerNodes), measuredSafeGroupSize, routeVersion)
	if err != nil {
		return nil, err
	}
	assignments := make([]NodeAssignment, 0, len(timerNodes))
	for groupID, sourceGroup := range sourceGroups {
		subjects := sourceGroup.Subjects
		externals := make(map[string]string, len(subjects))
		hashParts := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			external := strings.TrimSpace(group.ExternalSymbols[subject])
			resolved, symbolErr := stockProviderSymbol(subject, external)
			if symbolErr != nil {
				return nil, symbolErr
			}
			if external != "" && !strings.EqualFold(external, resolved) {
				externals[subject] = external
			}
			hashParts = append(hashParts, subject+"="+resolved)
		}
		node := timerNodes[groupID]
		provider := sourceGroup.Source.Provider
		sourceID := sourceGroup.Source.SourceID
		assignments = append(assignments, NodeAssignment{
			NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region,
			Provider: provider, RouteProvider: group.Provider, MarketType: group.MarketType, MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: sourceID, DatasetID: group.DatasetID, Frequency: group.Frequency,
			Subjects: append([]string(nil), subjects...), ExternalSymbols: externals, ProviderChain: []string{provider},
			RouteVersion: routeVersion, GroupID: groupID, GroupCount: expectedCount, Cron: stockCNStaggeredCron(groupID, stagger), Enabled: len(subjects) > 0,
			AssignmentHash: AssignmentHash(provider, sourceID, routeVersion, strconv.Itoa(groupID), group.MarketType, group.MarketID, group.InstrumentType, group.DatasetID, group.Frequency, strings.Join(hashParts, "|")),
		})
	}
	return assignments, nil
}

func disabledStockCNAssignments(group TaskGroup, nodes []scfinvoker.Node, routeVersion string, sources []stockCNSource, stagger StockCNStaggerConfig) ([]NodeAssignment, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("stock_cn disabled assignment requires a registered source")
	}
	source := sources[0]
	assignments := make([]NodeAssignment, 0, len(nodes))
	for groupID, node := range nodes {
		assignments = append(assignments, NodeAssignment{
			NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region,
			Provider: source.Provider, RouteProvider: group.Provider, MarketType: group.MarketType,
			MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: source.SourceID,
			DatasetID: group.DatasetID, Frequency: group.Frequency, ProviderChain: []string{source.Provider},
			RouteVersion: routeVersion, GroupID: groupID, GroupCount: len(nodes),
			Cron: stockCNStaggeredCron(groupID, stagger), Enabled: false,
			AssignmentHash: AssignmentHash(source.Provider, source.SourceID, routeVersion, strconv.Itoa(groupID), "disabled"),
		})
	}
	return assignments, nil
}

type stockCNSourceGroup struct {
	Source   stockCNSource
	Subjects []string
}

func assignStockCNSourceGroups(subjects []string, sources []stockCNSource, nodeCount, maxSubjects int, routeVersion string) ([]stockCNSourceGroup, error) {
	if nodeCount <= 0 || maxSubjects <= 0 {
		return nil, fmt.Errorf("stock_cn source assignment requires positive node count and subject capacity")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("stock_cn source assignment requires at least one source")
	}
	totalWeight := 0
	for _, source := range sources {
		source.Provider = strings.ToLower(strings.TrimSpace(source.Provider))
		source.SourceID = strings.ToLower(strings.TrimSpace(source.SourceID))
		if source.Provider == "" || source.SourceID == "" || source.Weight <= 0 {
			return nil, fmt.Errorf("invalid stock_cn source %q/%q", source.Provider, source.SourceID)
		}
		totalWeight += source.Weight
	}
	if totalWeight <= 0 {
		return nil, fmt.Errorf("stock_cn source weights must be positive")
	}
	normalized := normalizeSubjects(subjects)
	groups, err := marketdata.RendezvousAssign(normalized, nodeCount, routeVersion)
	if err != nil {
		return nil, fmt.Errorf("assign stock_cn groups: %w", err)
	}
	groups, err = rebalanceRendezvousGroups(groups, maxSubjects, routeVersion)
	if err != nil {
		return nil, err
	}
	result := make([]stockCNSourceGroup, nodeCount)
	for groupID, group := range groups {
		var source stockCNSource
		if groupID < len(sources) {
			source = sources[groupID]
		} else {
			source = weightedSourceBucket(routeVersion, strconv.Itoa(groupID), sources, totalWeight)
		}
		result[groupID] = stockCNSourceGroup{Source: source, Subjects: append([]string(nil), group...)}
	}
	return result, nil
}

func weightedSourceBucket(routeVersion, subject string, sources []stockCNSource, totalWeight int) stockCNSource {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(routeVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(subject))
	bucket := int(hash.Sum64() % uint64(totalWeight))
	for _, source := range sources {
		if bucket < source.Weight {
			return source
		}
		bucket -= source.Weight
	}
	return sources[len(sources)-1]
}

func requiredStockCNGroupSize(activeSubjects, timerFunctionCount int) (int, error) {
	if activeSubjects < 0 {
		return 0, fmt.Errorf("stock_cn active subject count must not be negative")
	}
	if timerFunctionCount <= 0 {
		return 0, fmt.Errorf("stock_cn timer function count must be positive")
	}
	if activeSubjects == 0 {
		return 0, nil
	}
	required := activeSubjects / timerFunctionCount
	if activeSubjects%timerFunctionCount != 0 {
		required++
	}
	return required, nil
}

func orderedStockCNTimerNodes(nodes []scfinvoker.Node, expectedCount int) ([]scfinvoker.Node, error) {
	if expectedCount <= 0 {
		return nil, fmt.Errorf("stock_cn expected timer function count must be positive")
	}
	if len(nodes) != expectedCount {
		return nil, fmt.Errorf("stock_cn timer fleet has %d nodes; expected %d", len(nodes), expectedCount)
	}
	type regionalNode struct {
		node scfinvoker.Node
		slot int
	}
	byRegion := make(map[string][]regionalNode)
	for _, node := range nodes {
		region := strings.TrimSpace(node.Region)
		name := strings.TrimSpace(node.FunctionName)
		separator := strings.LastIndexByte(name, '-')
		if separator < 0 || separator == len(name)-1 {
			return nil, fmt.Errorf("stock_cn timer node %s function_name %q has no numeric slot", node.NodeID, name)
		}
		slot, err := strconv.Atoi(name[separator+1:])
		if err != nil || slot < 0 {
			return nil, fmt.Errorf("stock_cn timer node %s function_name %q has invalid slot", node.NodeID, name)
		}
		if rawIndex, ok := node.Metadata["index"]; ok {
			metadataSlot, parseErr := strictInteger(rawIndex)
			if parseErr != nil || metadataSlot != slot {
				return nil, fmt.Errorf("stock_cn timer node %s slot %d does not match metadata index %v", node.NodeID, slot, rawIndex)
			}
		}
		byRegion[region] = append(byRegion[region], regionalNode{node: node, slot: slot})
	}
	regions := make([]string, 0, len(byRegion))
	for region := range byRegion {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	ordered := make([]scfinvoker.Node, 0, len(nodes))
	for _, region := range regions {
		regional := byRegion[region]
		sort.Slice(regional, func(i, j int) bool { return regional[i].slot < regional[j].slot })
		for expectedSlot, item := range regional {
			if item.slot != expectedSlot {
				return nil, fmt.Errorf("stock_cn timer fleet region %s is missing expected slot %d", region, expectedSlot)
			}
			ordered = append(ordered, item.node)
		}
	}
	return ordered, nil
}

func strictInteger(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		converted := int(typed)
		if float64(converted) != typed {
			return 0, fmt.Errorf("not an integer")
		}
		return converted, nil
	case string:
		return strconv.Atoi(strings.TrimSpace(typed))
	default:
		return 0, fmt.Errorf("unsupported integer type %T", value)
	}
}

func stockCNStaggeredCron(groupID int, configs ...StockCNStaggerConfig) string {
	stagger := DefaultStockCNStaggerConfig()
	if len(configs) > 0 {
		stagger = configs[0]
	}
	if stagger.WindowSeconds <= 0 {
		stagger.WindowSeconds = DefaultStockCNStaggerConfig().WindowSeconds
	}
	second := stagger.StartSecond + groupID%stagger.WindowSeconds
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
	if total >= len(result) {
		for empty := range result {
			if len(result[empty]) != 0 {
				continue
			}
			largest := -1
			for candidate := range result {
				if len(result[candidate]) <= 1 || (largest >= 0 && len(result[candidate]) <= len(result[largest])) {
					continue
				}
				largest = candidate
			}
			if largest >= 0 {
				last := len(result[largest]) - 1
				result[empty] = append(result[empty], result[largest][last])
				result[largest] = result[largest][:last]
			}
		}
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
		group.MarketID = strings.ToLower(strings.TrimSpace(group.MarketID))
		group.InstrumentType = strings.ToLower(strings.TrimSpace(group.InstrumentType))
		group.SourceID = strings.ToLower(strings.TrimSpace(group.SourceID))
		group.SeriesTag = strings.TrimSpace(group.SeriesTag)
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
		stockGroup := strings.EqualFold(group.MarketType, "equity") && group.DatasetID == StockCNDatasetID
		if !stockGroup && group.ExternalSymbols == nil {
			return nil, fmt.Errorf("task group external symbol mapping is required")
		}
		if !stockGroup {
			for _, subject := range group.Subjects {
				if strings.TrimSpace(group.ExternalSymbols[subject]) == "" {
					return nil, fmt.Errorf("subject %s has no external symbol", subject)
				}
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
		stockGroup := strings.EqualFold(group.MarketType, "equity") && group.DatasetID == StockCNDatasetID
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
				if stockGroup {
					resolved, symbolErr := stockProviderSymbol(subject, external)
					if symbolErr != nil {
						return nil, symbolErr
					}
					if external != "" && external != resolved {
						externals[subject] = external
					}
					hashParts = append(hashParts, subject+"="+resolved)
					continue
				}
				externals[subject] = external
				hashParts = append(hashParts, subject+"="+external)
			}
			node := timerNodes[nodeIndex]
			groupID := nodeIndex
			nodeIndex++
			assignments = append(assignments, NodeAssignment{
				NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region,
				Provider: group.Provider, RouteProvider: group.Provider, MarketType: group.MarketType,
				MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: group.SourceID, SeriesTag: group.SeriesTag,
				DatasetID: group.DatasetID, Frequency: group.Frequency, Subjects: subjects, ExternalSymbols: externals,
				GroupID: groupID, GroupCount: needed, Cron: cron, Enabled: true,
				AssignmentHash: AssignmentHash(group.Provider, group.MarketType, group.MarketID, group.InstrumentType, group.SourceID, group.SeriesTag, group.DatasetID, group.Frequency, strings.Join(hashParts, "|")),
			})
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
		if strings.EqualFold(strings.TrimSpace(node.NodeType), "scf-event") && strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") && !scfinvoker.IsInstrumentSnapshotNode(node) {
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
	return strings.Join([]string{group.Provider, group.MarketType, group.MarketID, group.InstrumentType, group.SourceID, group.SeriesTag, group.DatasetID, group.Frequency}, "\x00")
}

// AssignmentHash intentionally excludes timestamps so unchanged assignments
// do not cause an UpdateFunctionConfiguration call every reconciliation tick.
func AssignmentHash(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])[:16]
}

func CronForFrequency(frequency string) (string, error) {
	raw := strings.TrimSpace(frequency)
	if raw == "1M" {
		return "0 0 0 1 * * *", nil
	}
	switch strings.ToLower(raw) {
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
