package command

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

const (
	collectorTimerInventoryPageSize        = 1
	collectorTimerInventoryCatalogPageSize = 500
	collectorTimerInventoryReadbackWorkers = 4
	collectorTimerInventoryTimeout         = 15 * time.Minute
	collectorTimerReadbackMaxAge           = 5 * time.Minute
	collectorTimerTriggerType              = "timer"
	collectorTimerTriggerQualifier         = "$LATEST"
	collectorTimerTriggerMessage           = "market_fetch_timer_v1"
)

var collectorTimerInventoryNow = time.Now

type collectorTimerInventoryOptions struct {
	File           string
	SpaceID        string
	CloudAccountID string
	Namespace      string
	Region         string
}

type collectorTimerInventoryNode struct {
	NodeID               string `json:"node_id"`
	CloudAccountID       string `json:"cloud_account_id"`
	Namespace            string `json:"namespace"`
	Region               string `json:"region"`
	FunctionName         string `json:"function_name"`
	DesiredEnabled       *bool  `json:"desired_enabled"`
	DesiredCron          string `json:"desired_cron"`
	ActualEnabled        *bool  `json:"actual_enabled"`
	ActualCron           string `json:"actual_cron"`
	ConfigurationMatch   bool   `json:"configuration_match"`
	ActualTypeMatch      bool   `json:"actual_type_match"`
	ActualQualifierMatch bool   `json:"actual_qualifier_match"`
	ActualMessageMatch   bool   `json:"actual_message_match"`
	AvailableStatus      string `json:"available_status"`
	LastReadbackAt       string `json:"last_readback_at"`
	ReadbackFresh        bool   `json:"readback_fresh"`
	StatusErrorPresent   bool   `json:"status_error_present"`
}

type collectorTimerInventorySummary struct {
	SpaceID             string                        `json:"space_id"`
	CloudAccountID      string                        `json:"cloud_account_id"`
	Namespace           string                        `json:"namespace"`
	Region              string                        `json:"region"`
	StartedAt           string                        `json:"started_at"`
	ObservedAt          string                        `json:"observed_at"`
	TimerCount          int                           `json:"timer_count"`
	Complete            bool                          `json:"complete"`
	IntegrityIssueCount int                           `json:"integrity_issue_count"`
	Nodes               []collectorTimerInventoryNode `json:"nodes"`
}

var collectorTimerInventoryFlags collectorTimerInventoryOptions

var collectorFunctionTimerInventoryCmd = &cobra.Command{
	Use:   "timer-inventory",
	Short: "只读盘点指定 SCF Timer fleet 并校验云侧 readback",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), collectorTimerInventoryTimeout)
		defer cancel()
		if strings.TrimSpace(collectorTimerInventoryFlags.SpaceID) == "" {
			return fmt.Errorf("--space-id is required")
		}
		client, closeControl, err := useControlClient(nil, nil, collectorTimerInventoryFlags.File, collectorTimerInventoryFlags.SpaceID)
		if err != nil {
			return err
		}
		defer closeControl()
		summary, err := inspectCollectorTimerInventory(ctx, client, collectorTimerInventoryFlags)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if encodeErr := enc.Encode(summary); encodeErr != nil {
			return encodeErr
		}
		return err
	},
}

func init() {
	collectorFunctionCmd.AddCommand(collectorFunctionTimerInventoryCmd)
	flags := collectorFunctionTimerInventoryCmd.Flags()
	flags.StringVar(&collectorTimerInventoryFlags.File, "file", "", "moox.toml；访问控制面所需的 SSH 连接信息取自此文件，默认读当前目录的 moox.toml")
	flags.StringVar(&collectorTimerInventoryFlags.SpaceID, "space-id", "", "required space id")
	flags.StringVar(&collectorTimerInventoryFlags.CloudAccountID, "cloud-account-id", "", "required SCF cloud account id")
	flags.StringVar(&collectorTimerInventoryFlags.Namespace, "namespace", "", "required SCF namespace")
	flags.StringVar(&collectorTimerInventoryFlags.Region, "region", "", "required SCF region")
}

func inspectCollectorTimerInventory(ctx context.Context, client *adminclient.Client, opts collectorTimerInventoryOptions) (*collectorTimerInventorySummary, error) {
	if client == nil {
		return nil, fmt.Errorf("Control client is unavailable")
	}
	for name, value := range map[string]string{
		"space id": opts.SpaceID, "cloud account id": opts.CloudAccountID, "namespace": opts.Namespace, "region": opts.Region,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required for a bounded Timer inventory", name)
		}
	}
	startedAt := collectorTimerInventoryNow().UTC()
	summary := &collectorTimerInventorySummary{
		SpaceID: opts.SpaceID, CloudAccountID: opts.CloudAccountID, Namespace: opts.Namespace, Region: opts.Region,
		StartedAt: startedAt.Format(time.RFC3339Nano), Nodes: []collectorTimerInventoryNode{},
	}
	catalog, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: opts.CloudAccountID,
		Namespace:      opts.Namespace,
		Region:         opts.Region,
		NodeType:       "scf-event",
		BizType:        "market_fetcher",
		TriggerType:    "timer",
		PageSize:       collectorTimerInventoryCatalogPageSize,
	})
	if err != nil {
		return summary, fmt.Errorf("list scoped market_fetcher Timer catalog: %w", err)
	}
	complete := true
	seenNodeIDs := make(map[string]struct{}, len(catalog))
	targets := make([]adminclient.CloudNode, 0, len(catalog))
	for _, node := range catalog {
		if node.IsDeleted {
			continue
		}
		if !collectorTimerNodeMatchesScope(node, opts) || !collectorTimerNodeIsMarketFetcher(node) {
			summary.IntegrityIssueCount++
			complete = false
			continue
		}
		if _, duplicate := seenNodeIDs[node.NodeID]; duplicate {
			summary.IntegrityIssueCount++
			complete = false
			continue
		}
		seenNodeIDs[node.NodeID] = struct{}{}
		targets = append(targets, node)
	}
	if len(targets) == 0 {
		complete = false
	}
	readbacks := readCollectorTimerNodes(ctx, client, opts, targets)
	observedAt := collectorTimerInventoryNow().UTC()
	summary.ObservedAt = observedAt.Format(time.RFC3339Nano)
	for index, target := range targets {
		readback := readbacks[index]
		node := target
		if readback.found {
			node = readback.node
		} else {
			complete = false
		}
		if readback.integrityIssue {
			summary.IntegrityIssueCount++
		}
		row, valid := collectorTimerInventoryRow(node, startedAt, observedAt)
		if !valid {
			complete = false
		}
		summary.Nodes = append(summary.Nodes, row)
	}
	summary.TimerCount = len(summary.Nodes)
	summary.Complete = complete
	sort.Slice(summary.Nodes, func(i, j int) bool {
		if summary.Nodes[i].Region != summary.Nodes[j].Region {
			return summary.Nodes[i].Region < summary.Nodes[j].Region
		}
		return summary.Nodes[i].NodeID < summary.Nodes[j].NodeID
	})
	if !complete {
		return summary, fmt.Errorf("Timer inventory is incomplete: empty scope, provider readback, configuration match, or available status check failed")
	}
	return summary, nil
}

type collectorTimerInventoryReadback struct {
	node           adminclient.CloudNode
	found          bool
	integrityIssue bool
}

func readCollectorTimerNodes(ctx context.Context, client *adminclient.Client, opts collectorTimerInventoryOptions, targets []adminclient.CloudNode) []collectorTimerInventoryReadback {
	results := make([]collectorTimerInventoryReadback, len(targets))
	if len(targets) == 0 {
		return results
	}
	workerCount := min(collectorTimerInventoryReadbackWorkers, len(targets))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				target := targets[index]
				matches, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
					CloudAccountID: opts.CloudAccountID,
					Namespace:      opts.Namespace,
					Region:         opts.Region,
					NodeID:         target.NodeID,
					NodeType:       "scf-event",
					TriggerType:    "timer",
					PageSize:       collectorTimerInventoryPageSize,
				})
				if err != nil {
					results[index].node = target
					continue
				}
				var exact *adminclient.CloudNode
				for itemIndex := range matches {
					if matches[itemIndex].NodeID != target.NodeID {
						continue
					}
					if exact != nil {
						exact = nil
						break
					}
					exact = &matches[itemIndex]
				}
				if exact == nil || exact.IsDeleted || !collectorTimerNodeMatchesScope(*exact, opts) || !collectorTimerNodeIsMarketFetcher(*exact) {
					results[index] = collectorTimerInventoryReadback{node: target, integrityIssue: true}
					continue
				}
				results[index] = collectorTimerInventoryReadback{node: *exact, found: true}
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results
}

func collectorTimerInventoryRow(node adminclient.CloudNode, startedAt, observedAt time.Time) (collectorTimerInventoryNode, bool) {
	desiredEnabled, hasDesiredEnabled := collectorMetadataBool(node.Metadata, "timer_enabled")
	actualEnabled, hasActualEnabled := collectorMetadataBool(node.Metadata, "timer_actual_enabled")
	row := collectorTimerInventoryNode{
		NodeID: node.NodeID, CloudAccountID: node.CloudAccountID, Namespace: node.Namespace, Region: node.Region,
		FunctionName: node.FunctionName, DesiredCron: metadataStringValue(node.Metadata, "timer_cron"),
		ActualCron:      metadataStringValue(node.Metadata, "timer_actual_cron"),
		AvailableStatus: metadataStringValue(node.Metadata, "timer_available_status"),
		LastReadbackAt:  metadataStringValue(node.Metadata, "timer_last_readback_at"),
	}
	if hasDesiredEnabled {
		row.DesiredEnabled = &desiredEnabled
	}
	if hasActualEnabled {
		row.ActualEnabled = &actualEnabled
	}
	if statusErr := node.Metadata["timer_status_error"]; statusErr != nil && strings.TrimSpace(fmt.Sprint(statusErr)) != "" {
		row.StatusErrorPresent = true
	}
	lastReadback, parseErr := time.Parse(time.RFC3339Nano, row.LastReadbackAt)
	row.ReadbackFresh = parseErr == nil && !lastReadback.Before(startedAt) &&
		observedAt.Sub(lastReadback) <= collectorTimerReadbackMaxAge && !lastReadback.After(observedAt.Add(time.Minute))
	row.ConfigurationMatch = hasDesiredEnabled && hasActualEnabled && desiredEnabled == actualEnabled &&
		strings.TrimSpace(row.DesiredCron) != "" && strings.TrimSpace(row.DesiredCron) == strings.TrimSpace(row.ActualCron)
	row.ActualTypeMatch = strings.EqualFold(strings.TrimSpace(metadataStringValue(node.Metadata, "timer_actual_type")), collectorTimerTriggerType)
	row.ActualQualifierMatch = strings.TrimSpace(metadataStringValue(node.Metadata, "timer_actual_qualifier")) == collectorTimerTriggerQualifier
	row.ActualMessageMatch = strings.TrimSpace(metadataStringValue(node.Metadata, "timer_actual_message")) == collectorTimerTriggerMessage
	valid := row.ConfigurationMatch && row.ActualTypeMatch && row.ActualQualifierMatch && row.ActualMessageMatch &&
		row.ReadbackFresh && !row.StatusErrorPresent && timerInventoryAvailable(row.AvailableStatus)
	return row, valid
}

func collectorTimerNodeMatchesScope(node adminclient.CloudNode, opts collectorTimerInventoryOptions) bool {
	return strings.TrimSpace(node.NodeID) != "" &&
		strings.TrimSpace(node.CloudAccountID) == strings.TrimSpace(opts.CloudAccountID) &&
		strings.TrimSpace(node.Namespace) == strings.TrimSpace(opts.Namespace) &&
		strings.TrimSpace(node.Region) == strings.TrimSpace(opts.Region) &&
		strings.EqualFold(strings.TrimSpace(node.NodeType), "scf-event") &&
		strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer")
}

func collectorTimerNodeIsMarketFetcher(node adminclient.CloudNode) bool {
	return strings.EqualFold(strings.TrimSpace(node.BizType), "market_fetcher")
}

func timerInventoryAvailable(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "available")
}
