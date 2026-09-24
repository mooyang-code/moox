package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

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
		return fmt.Errorf("stockcn timer function count must be positive")
	}
	if c.StartSecond < 0 || c.StartSecond > 59 {
		return fmt.Errorf("stockcn stagger start second must be between 0 and 59")
	}
	if c.WindowSeconds <= 0 || c.WindowSeconds > 60 || c.StartSecond+c.WindowSeconds > 60 {
		return fmt.Errorf("stockcn stagger window must fit between second %d and 59", c.StartSecond)
	}
	if c.MaxStartsPerSecond <= 0 {
		return fmt.Errorf("stockcn max starts per second must be positive")
	}
	startsPerSecond := (timerFunctionCount + c.WindowSeconds - 1) / c.WindowSeconds
	if startsPerSecond > c.MaxStartsPerSecond {
		return fmt.Errorf("stockcn stagger requires up to %d starts per second, above configured maximum %d", startsPerSecond, c.MaxStartsPerSecond)
	}
	return nil
}

func stockCNAssignmentRoute() (string, []stockCNSource, error) {
	route, err := loadStockCNRoute()
	if err != nil {
		return "", nil, fmt.Errorf("load stockcn assignment route: %w", err)
	}
	sources := route.KlinePrimarySources()
	if len(sources) < 2 {
		return "", nil, fmt.Errorf("stockcn assignment route must have at least two primary kline providers")
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
		return nil, fmt.Errorf("stockcn measured safe group size must be positive")
	}
	if len(expectedCounts) != 1 || expectedCounts[0] <= 0 {
		return nil, fmt.Errorf("stockcn requires an explicit positive timer function count")
	}
	group.Provider = strings.ToLower(strings.TrimSpace(group.Provider))
	group.MarketType = strings.ToLower(strings.TrimSpace(group.MarketType))
	group.DatasetID = strings.TrimSpace(group.DatasetID)
	group.Frequency = strings.ToLower(strings.TrimSpace(group.Frequency))
	group.Subjects = normalizeSubjects(group.Subjects)
	if group.MarketType != "equity" || group.DatasetID != StockCNDatasetID || group.Frequency != "1m" {
		return nil, fmt.Errorf("stockcn assignment requires equity/%s/1m", StockCNDatasetID)
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
		return nil, fmt.Errorf("timer assignment capacity insufficient: stockcn requires a published Timer SCF fleet")
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
		return nil, fmt.Errorf("stockcn disabled assignment requires a registered source")
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
		return nil, fmt.Errorf("stockcn source assignment requires positive node count and subject capacity")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("stockcn source assignment requires at least one source")
	}
	totalWeight := 0
	for _, source := range sources {
		source.Provider = strings.ToLower(strings.TrimSpace(source.Provider))
		source.SourceID = strings.ToLower(strings.TrimSpace(source.SourceID))
		if source.Provider == "" || source.SourceID == "" || source.Weight <= 0 {
			return nil, fmt.Errorf("invalid stockcn source %q/%q", source.Provider, source.SourceID)
		}
		totalWeight += source.Weight
	}
	if totalWeight <= 0 {
		return nil, fmt.Errorf("stockcn source weights must be positive")
	}
	groups, err := assignStockCNGroups(normalizeSubjects(subjects), nodeCount, maxSubjects, routeVersion)
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

// assignStockCNGroups gives the first subject in each published slot a
// distinct group, then uses bounded rendezvous scoring for the remainder.
// Keeping the anchors stable makes an append-only Instrument refresh change
// only the new subject instead of reshuffling a previously published fleet.
func assignStockCNGroups(subjects []string, nodeCount, maxSubjects int, routeVersion string) ([][]string, error) {
	if nodeCount <= 0 || maxSubjects <= 0 {
		return nil, fmt.Errorf("stockcn rendezvous capacity must be positive")
	}
	if len(subjects) > nodeCount*maxSubjects {
		return nil, fmt.Errorf("stockcn timer assignment capacity insufficient: %d subjects exceed %d groups x %d subjects", len(subjects), nodeCount, maxSubjects)
	}

	groups := make([][]string, nodeCount)
	for index, subject := range subjects {
		candidates := make([]int, 0, nodeCount)
		for groupID, group := range groups {
			if index < nodeCount {
				if len(group) == 0 {
					candidates = append(candidates, groupID)
				}
				continue
			}
			if len(group) < maxSubjects {
				candidates = append(candidates, groupID)
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("stockcn subject %s has no available assignment group", subject)
		}
		selected := candidates[0]
		selectedScore := stockCNGroupScore(routeVersion, subject, selected)
		for _, groupID := range candidates[1:] {
			score := stockCNGroupScore(routeVersion, subject, groupID)
			if score > selectedScore || (score == selectedScore && groupID < selected) {
				selected = groupID
				selectedScore = score
			}
		}
		groups[selected] = append(groups[selected], subject)
	}
	return groups, nil
}

func stockCNGroupScore(routeVersion, subject string, groupID int) uint64 {
	hash := fnv.New64a()
	for _, part := range []string{routeVersion, subject, strconv.Itoa(groupID)} {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum64()
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
		return 0, fmt.Errorf("stockcn active subject count must not be negative")
	}
	if timerFunctionCount <= 0 {
		return 0, fmt.Errorf("stockcn timer function count must be positive")
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
		return nil, fmt.Errorf("stockcn expected timer function count must be positive")
	}
	if len(nodes) != expectedCount {
		return nil, fmt.Errorf("stockcn timer fleet has %d nodes; expected %d", len(nodes), expectedCount)
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
			return nil, fmt.Errorf("stockcn timer node %s function_name %q has no numeric slot", node.NodeID, name)
		}
		slot, err := strconv.Atoi(name[separator+1:])
		if err != nil || slot < 0 {
			return nil, fmt.Errorf("stockcn timer node %s function_name %q has invalid slot", node.NodeID, name)
		}
		if rawIndex, ok := node.Metadata["index"]; ok {
			metadataSlot, parseErr := strictInteger(rawIndex)
			if parseErr != nil || metadataSlot != slot {
				return nil, fmt.Errorf("stockcn timer node %s slot %d does not match metadata index %v", node.NodeID, slot, rawIndex)
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
				return nil, fmt.Errorf("stockcn timer fleet region %s is missing expected slot %d", region, expectedSlot)
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
	planned := make([][][]string, len(normalized))
	needed := 0
	swapNeeded := 0
	otherNeeded := 0
	swapAllow := make(map[int]bool)
	otherAllow := make(map[int]bool)
	for index, group := range normalized {
		subjects := group.Subjects
		if isCryptoKlineGroup(group) {
			subjects = pinPriorityCryptoSubjects(subjects)
		}
		planned[index] = splitSubjectChunks(subjects, maxSubjects)
		count := len(planned[index])
		needed += count
		if requiresOverseasEgress(group) {
			swapNeeded += count
			swapAllow[index] = true
		} else {
			otherNeeded += count
			otherAllow[index] = true
		}
	}
	overseasNodes, mainlandNodes := partitionTimerNodesByEgress(timerNodes)
	pinOverseas := len(overseasNodes) > 0 && swapNeeded > 0
	if pinOverseas {
		if swapNeeded > len(overseasNodes) {
			return nil, fmt.Errorf("timer assignment capacity insufficient: %d overseas Timer nodes are required for Binance swap shards, but only %d are available; increase the Hong Kong/Singapore Timer fleet or drop lower-priority swap frequencies", swapNeeded, len(overseasNodes))
		}
		if otherNeeded > len(timerNodes)-swapNeeded {
			return nil, fmt.Errorf("timer assignment capacity insufficient: %d Timer nodes are required for the configured dataset/frequency shards, but only %d are available; increase the Timer SCF fleet or drop lower-priority frequencies", needed, len(timerNodes))
		}
		swapNeeded += applyCryptoSpare(normalized, planned, swapAllow, len(overseasNodes)-swapNeeded, maxSubjects)
		otherNeeded += applyCryptoSpare(normalized, planned, otherAllow, len(timerNodes)-swapNeeded-otherNeeded, maxSubjects)
		needed = swapNeeded + otherNeeded
	} else {
		if needed > len(timerNodes) {
			return nil, fmt.Errorf("timer assignment capacity insufficient: %d Timer nodes are required for the configured dataset/frequency shards, but only %d are available; increase the Timer SCF fleet or drop lower-priority frequencies", needed, len(timerNodes))
		}
		needed += applyCryptoSpare(normalized, planned, nil, len(timerNodes)-needed, maxSubjects)
	}
	queues := assignmentNodeQueues(timerNodes, overseasNodes, mainlandNodes, pinOverseas)
	assignments := make([]NodeAssignment, 0, len(timerNodes))
	groupID := 0
	used := make(map[string]struct{}, len(timerNodes))
	for _, index := range overseasFirstIndexes(normalized) {
		group := normalized[index]
		for _, subjects := range planned[index] {
			node, err := takeAssignmentNode(&queues, requiresOverseasEgress(group), pinOverseas)
			if err != nil {
				return nil, err
			}
			assignment, assignErr := assignmentFromChunk(group, subjects, node, groupID, needed)
			if assignErr != nil {
				return nil, assignErr
			}
			assignments = append(assignments, assignment)
			used[node.NodeID] = struct{}{}
			groupID++
		}
	}
	for _, node := range timerNodes {
		if _, ok := used[node.NodeID]; ok {
			continue
		}
		assignments = append(assignments, NodeAssignment{NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region, Enabled: false, AssignmentHash: AssignmentHash()})
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].NodeID < assignments[j].NodeID })
	return assignments, nil
}

type assignmentQueues struct {
	overseas []scfinvoker.Node
	remain   []scfinvoker.Node
	shared   []scfinvoker.Node
}

func assignmentNodeQueues(all, overseas, mainland []scfinvoker.Node, pinOverseas bool) assignmentQueues {
	if !pinOverseas {
		return assignmentQueues{shared: append([]scfinvoker.Node(nil), all...)}
	}
	return assignmentQueues{
		overseas: append([]scfinvoker.Node(nil), overseas...),
		remain:   append(append([]scfinvoker.Node(nil), mainland...), overseas...),
	}
}

func takeAssignmentNode(queues *assignmentQueues, overseasOnly, pinOverseas bool) (scfinvoker.Node, error) {
	if queues == nil {
		return scfinvoker.Node{}, fmt.Errorf("timer assignment has no node queue")
	}
	if !pinOverseas {
		if len(queues.shared) == 0 {
			return scfinvoker.Node{}, fmt.Errorf("timer assignment capacity insufficient: no Timer node remains")
		}
		node := queues.shared[0]
		queues.shared = queues.shared[1:]
		return node, nil
	}
	if overseasOnly {
		if len(queues.overseas) == 0 {
			return scfinvoker.Node{}, fmt.Errorf("timer assignment capacity insufficient: no overseas Timer node remains for Binance swap")
		}
		node := queues.overseas[0]
		queues.overseas = queues.overseas[1:]
		queues.remain = removeNodeByID(queues.remain, node.NodeID)
		return node, nil
	}
	if len(queues.remain) == 0 {
		return scfinvoker.Node{}, fmt.Errorf("timer assignment capacity insufficient: no Timer node remains")
	}
	node := queues.remain[0]
	queues.remain = queues.remain[1:]
	queues.overseas = removeNodeByID(queues.overseas, node.NodeID)
	return node, nil
}

func overseasFirstIndexes(groups []TaskGroup) []int {
	indexes := make([]int, 0, len(groups))
	for index, group := range groups {
		if requiresOverseasEgress(group) {
			indexes = append(indexes, index)
		}
	}
	for index, group := range groups {
		if !requiresOverseasEgress(group) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func removeNodeByID(nodes []scfinvoker.Node, nodeID string) []scfinvoker.Node {
	out := nodes[:0]
	for _, node := range nodes {
		if node.NodeID == nodeID {
			continue
		}
		out = append(out, node)
	}
	return out
}

func assignmentFromChunk(group TaskGroup, subjects []string, node scfinvoker.Node, groupID, needed int) (NodeAssignment, error) {
	stockGroup := strings.EqualFold(group.MarketType, "equity") && group.DatasetID == StockCNDatasetID
	hashParts := make([]string, 0, len(subjects))
	externals := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		external := strings.TrimSpace(group.ExternalSymbols[subject])
		if stockGroup {
			resolved, symbolErr := stockProviderSymbol(subject, external)
			if symbolErr != nil {
				return NodeAssignment{}, symbolErr
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
	return NodeAssignment{
		NodeID: node.NodeID, FunctionName: node.FunctionName, Region: node.Region,
		Provider: group.Provider, RouteProvider: group.Provider, MarketType: group.MarketType,
		MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: group.SourceID, SeriesTag: group.SeriesTag,
		DatasetID: group.DatasetID, Frequency: group.Frequency, Subjects: subjects, ExternalSymbols: externals,
		GroupID: groupID, GroupCount: needed, Cron: assignmentCron(group, groupID), Enabled: true,
		AssignmentHash: AssignmentHash(group.Provider, group.MarketType, group.MarketID, group.InstrumentType, group.SourceID, group.SeriesTag, group.DatasetID, group.Frequency, strings.Join(hashParts, "|")),
	}, nil
}

func applyCryptoSpare(normalized []TaskGroup, planned [][][]string, allow map[int]bool, spare, maxSubjects int) int {
	added := 0
	for _, index := range cryptoSpareAllocationOrder(normalized) {
		if spare <= 0 {
			break
		}
		if allow != nil && !allow[index] {
			continue
		}
		chunks := planCryptoSubjectChunks(normalized[index].Subjects, maxSubjects, spare)
		extra := len(chunks) - len(planned[index])
		if extra <= 0 || extra > spare {
			continue
		}
		spare -= extra
		added += extra
		planned[index] = chunks
	}
	return added
}

func requiresOverseasEgress(group TaskGroup) bool {
	// Binance is an overseas provider for both spot and perpetual markets.
	// Keep all crypto K-line traffic in an overseas SCF region so the provider
	// request and the regional Storage Access hop use the same egress path.
	dataset := strings.ToLower(strings.TrimSpace(group.DatasetID))
	if strings.EqualFold(strings.TrimSpace(group.Provider), "binance") &&
		(strings.EqualFold(strings.TrimSpace(group.MarketID), "crypto") || strings.Contains(dataset, "binance_")) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(group.MarketType), "swap") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(group.InstrumentType), "swap") {
		return true
	}
	return strings.Contains(dataset, "_swap_") || strings.Contains(dataset, "swap_kline")
}

func isOverseasSCFRegion(region string) bool {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "ap-hongkong", "ap-singapore", "ap-tokyo", "ap-seoul", "ap-bangkok", "ap-jakarta",
		"eu-frankfurt", "na-ashburn", "na-siliconvalley", "sa-saopaulo":
		return true
	default:
		return false
	}
}

func partitionTimerNodesByEgress(nodes []scfinvoker.Node) (overseas, mainland []scfinvoker.Node) {
	for _, node := range nodes {
		if isOverseasSCFRegion(node.Region) {
			overseas = append(overseas, node)
		} else {
			mainland = append(mainland, node)
		}
	}
	return roundRobinRegions(overseas), roundRobinRegions(mainland)
}

var priorityCryptoSubjects = []string{"BTC-USDT", "ETH-USDT"}

func isCryptoKlineGroup(group TaskGroup) bool {
	if strings.EqualFold(strings.TrimSpace(group.MarketID), "crypto") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(group.MarketType)) {
	case "spot", "swap":
		return true
	default:
		return false
	}
}

func isCryptoKlineAssignment(assignment NodeAssignment) bool {
	return isCryptoKlineGroup(TaskGroup{MarketType: assignment.MarketType, MarketID: assignment.MarketID})
}

func cryptoSpareAllocationOrder(groups []TaskGroup) []int {
	indexes := make([]int, 0, len(groups))
	for index, group := range groups {
		if isCryptoKlineGroup(group) {
			indexes = append(indexes, index)
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := groups[indexes[i]], groups[indexes[j]]
		leftRank, rightRank := cryptoFrequencySpareRank(left.Frequency), cryptoFrequencySpareRank(right.Frequency)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return groupKey(left) < groupKey(right)
	})
	return indexes
}

func cryptoFrequencySpareRank(freq string) int {
	switch strings.ToLower(strings.TrimSpace(freq)) {
	case "1m":
		return 0
	case "5m":
		return 1
	case "15m":
		return 2
	case "30m":
		return 3
	case "1h":
		return 10
	default:
		return 50
	}
}

func pinPriorityCryptoSubjects(subjects []string) []string {
	if len(subjects) == 0 {
		return subjects
	}
	present := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		present[subject] = struct{}{}
	}
	out := make([]string, 0, len(subjects))
	seen := make(map[string]struct{}, len(priorityCryptoSubjects))
	for _, subject := range priorityCryptoSubjects {
		if _, ok := present[subject]; !ok {
			continue
		}
		out = append(out, subject)
		seen[subject] = struct{}{}
	}
	for _, subject := range subjects {
		if _, ok := seen[subject]; ok {
			continue
		}
		out = append(out, subject)
	}
	return out
}

func splitSubjectChunks(subjects []string, maxSubjects int) [][]string {
	if maxSubjects <= 0 || len(subjects) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(subjects)+maxSubjects-1)/maxSubjects)
	for start := 0; start < len(subjects); start += maxSubjects {
		end := start + maxSubjects
		if end > len(subjects) {
			end = len(subjects)
		}
		chunks = append(chunks, append([]string(nil), subjects[start:end]...))
	}
	return chunks
}

func planCryptoSubjectChunks(subjects []string, maxSubjects, spare int) [][]string {
	remaining := pinPriorityCryptoSubjects(subjects)
	solos := make([][]string, 0, len(priorityCryptoSubjects))
	for _, subject := range priorityCryptoSubjects {
		if spare <= 0 || !cryptoGroupHasNonPriority(remaining) {
			break
		}
		index := indexOfSubject(remaining, subject)
		if index < 0 {
			continue
		}
		candidate := append(append([]string{}, remaining[:index]...), remaining[index+1:]...)
		oldTotal := len(solos) + (len(remaining)+maxSubjects-1)/maxSubjects
		newTotal := len(solos) + 1
		if len(candidate) > 0 {
			newTotal += (len(candidate) + maxSubjects - 1) / maxSubjects
		}
		extra := newTotal - oldTotal
		if extra > spare {
			continue
		}
		spare -= extra
		solos = append(solos, []string{subject})
		remaining = candidate
	}
	if len(remaining) == 0 {
		return solos
	}
	return append(solos, splitSubjectChunks(remaining, maxSubjects)...)
}

func cryptoGroupHasNonPriority(subjects []string) bool {
	priority := make(map[string]struct{}, len(priorityCryptoSubjects))
	for _, subject := range priorityCryptoSubjects {
		priority[subject] = struct{}{}
	}
	for _, subject := range subjects {
		if _, ok := priority[subject]; !ok {
			return true
		}
	}
	return false
}

func indexOfSubject(subjects []string, want string) int {
	for index, subject := range subjects {
		if subject == want {
			return index
		}
	}
	return -1
}

// assignmentCron spreads crypto hourly shards across the first ten minutes
// of the hour. A large fleet otherwise invokes every 1h function at exactly
// :00, which can exhaust the provider/storage request budget and leave a
// whole hour missing even though the timer itself is enabled. Minute bars
// retain their every-minute schedule; other frequencies keep their canonical
// cadence.
func assignmentCron(group TaskGroup, groupID int) string {
	cron, _ := CronForFrequency(group.Frequency)
	if strings.EqualFold(group.MarketID, "crypto") && strings.EqualFold(group.Frequency, "1h") {
		return fmt.Sprintf("0 %d * * * * *", groupID%10)
	}
	return cron
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

func cryptoTimerGroupRank(frequency string) int {
	switch strings.ToLower(strings.TrimSpace(frequency)) {
	case "1m", "1min", "1minute":
		return 0
	default:
		return 1
	}
}

func assignmentShardCount(groups []TaskGroup, maxSubjects int) int {
	needed := 0
	for _, group := range groups {
		subjects := group.Subjects
		if isCryptoKlineGroup(group) {
			subjects = pinPriorityCryptoSubjects(subjects)
		}
		needed += len(splitSubjectChunks(subjects, maxSubjects))
	}
	return needed
}

// selectCryptoGroupsForCapacity keeps higher-priority frequencies when the
// Timer fleet cannot cover every dataset/frequency shard. Minute bars stay
// assigned; hourly and slower groups are deferred instead of failing the
// whole reconciliation and stopping all collection.
func selectCryptoGroupsForCapacity(groups []TaskGroup, nodes []scfinvoker.Node, maxSubjects int) ([]TaskGroup, []TaskGroup, error) {
	if maxSubjects <= 0 {
		return nil, nil, fmt.Errorf("max subjects must be positive")
	}
	timerNodes := eligibleTimerNodes(nodes)
	if len(timerNodes) == 0 {
		timerNodes = append([]scfinvoker.Node(nil), nodes...)
	}
	nodeCount := len(timerNodes)
	overseasCount := 0
	for _, node := range timerNodes {
		if isOverseasSCFRegion(node.Region) {
			overseasCount++
		}
	}
	ordered := append([]TaskGroup(nil), groups...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if rankI, rankJ := cryptoTimerGroupRank(ordered[i].Frequency), cryptoTimerGroupRank(ordered[j].Frequency); rankI != rankJ {
			return rankI < rankJ
		}
		return groupKey(ordered[i]) < groupKey(ordered[j])
	})
	selected := make([]TaskGroup, 0, len(ordered))
	deferred := make([]TaskGroup, 0)
	usedOverseas := 0
	usedOther := 0
	pinOverseas := overseasCount > 0
	for _, group := range ordered {
		need := assignmentShardCount([]TaskGroup{group}, maxSubjects)
		if need == 0 || !pinOverseas || !requiresOverseasEgress(group) {
			continue
		}
		if usedOverseas+need <= overseasCount {
			selected = append(selected, group)
			usedOverseas += need
			continue
		}
		deferred = append(deferred, group)
	}
	for _, group := range ordered {
		need := assignmentShardCount([]TaskGroup{group}, maxSubjects)
		if need == 0 || (pinOverseas && requiresOverseasEgress(group)) {
			continue
		}
		if usedOther+need <= nodeCount-usedOverseas {
			selected = append(selected, group)
			usedOther += need
			continue
		}
		deferred = append(deferred, group)
	}
	if len(selected) == 0 {
		return nil, deferred, fmt.Errorf("timer assignment capacity insufficient: %d Timer nodes are required for the configured dataset/frequency shards, but only %d are available; increase the Timer SCF fleet", assignmentShardCount(groups, maxSubjects), nodeCount)
	}
	return selected, deferred, nil
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
