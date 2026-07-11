package cmd

import (
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

const (
	jobItemPending = 1
	jobItemRunning = 2
)

var collectorLegacyCutoverFlags struct {
	Mode, LegacySpace, ControlURL string
}

type legacyCutoverSummary struct {
	Mode             string   `json:"mode"`
	LegacySpace      string   `json:"legacy_space"`
	PendingCanceled  int      `json:"pending_canceled"`
	NodesDeleted     int      `json:"nodes_deleted"`
	RunningJobItems  []string `json:"running_job_items"`
	RetainedNodeIDs  []string `json:"retained_node_ids"`
	RollbackRequired bool     `json:"rollback_required"`
}

var collectorLegacyCutoverCmd = &cobra.Command{
	Use:   "legacy-cutover",
	Short: "Preflight or drain legacy collector work before Market V2 cutover",
	RunE: func(cmd *cobra.Command, _ []string) error {
		summary, err := runLegacyCutover(cmd, collectorLegacyCutoverFlags.Mode, collectorLegacyCutoverFlags.LegacySpace, collectorLegacyCutoverFlags.ControlURL)
		if err != nil {
			return err
		}
		return writeJSON(cmd, summary)
	},
}

func runLegacyCutover(cmd *cobra.Command, mode, legacySpace, controlURL string) (legacyCutoverSummary, error) {
	summary := legacyCutoverSummary{Mode: mode, LegacySpace: legacySpace}
	if mode != "preflight" && mode != "drain" && mode != "finalize" && mode != "rollback" {
		return summary, fmt.Errorf("--mode must be preflight, drain, finalize or rollback")
	}
	if legacySpace == "" || controlURL == "" {
		return summary, fmt.Errorf("--legacy-space and --control-url are required")
	}
	client := newControlClient(controlURL, "", os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"), os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"), legacySpace)
	if mode == "rollback" {
		// Drain deliberately retains legacy nodes/functions. Stopping the old
		// Collector is the cutover boundary, so rollback has no cloud mutation to undo.
		return summary, nil
	}

	pending, err := listAllLegacyJobItems(cmd, client, jobItemPending)
	if err != nil {
		return summary, err
	}
	running, err := listAllLegacyJobItems(cmd, client, jobItemRunning)
	if err != nil {
		return summary, err
	}
	for _, item := range running {
		summary.RunningJobItems = append(summary.RunningJobItems, item.JobItemID)
	}
	nodes, err := listAllLegacyNodes(cmd, client)
	if err != nil {
		return summary, err
	}
	for _, node := range nodes {
		summary.RetainedNodeIDs = append(summary.RetainedNodeIDs, node.NodeID)
	}
	if mode == "drain" {
		for _, item := range pending {
			if err := client.CancelJobItem(cmd.Context(), item.JobItemID); err != nil {
				return summary, fmt.Errorf("cancel pending job item %s: %w", item.JobItemID, err)
			}
			summary.PendingCanceled++
		}
	}
	if len(summary.RunningJobItems) > 0 {
		return summary, fmt.Errorf("legacy space %s still has %d running job items", legacySpace, len(summary.RunningJobItems))
	}
	if mode == "finalize" {
		if len(pending) > 0 {
			return summary, fmt.Errorf("legacy space %s still has %d pending job items; run drain first", legacySpace, len(pending))
		}
		for start := 0; start < len(nodes); start += 20 {
			end := min(start+20, len(nodes))
			ids := make([]string, 0, end-start)
			for _, node := range nodes[start:end] {
				ids = append(ids, node.NodeID)
			}
			if _, err := client.BatchDeleteNodes(cmd.Context(), ids); err != nil {
				return summary, fmt.Errorf("delete legacy nodes: %w", err)
			}
			summary.NodesDeleted += len(ids)
		}
	}
	return summary, nil
}

func listAllLegacyJobItems(cmd *cobra.Command, client *adminclient.Client, status int) ([]adminclient.JobItem, error) {
	var out []adminclient.JobItem
	for page := 1; ; page++ {
		items, more, err := client.ListJobItems(cmd.Context(), status, page, 200)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if !more {
			return out, nil
		}
	}
}

func listAllLegacyNodes(cmd *cobra.Command, client *adminclient.Client) ([]adminclient.CloudNode, error) {
	var out []adminclient.CloudNode
	for page := 1; ; page++ {
		items, more, err := client.ListNodes(cmd.Context(), page, 200)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if !more {
			return out, nil
		}
	}
}

func init() {
	collectorCmd.AddCommand(collectorLegacyCutoverCmd)
	f := collectorLegacyCutoverCmd.Flags()
	f.StringVar(&collectorLegacyCutoverFlags.Mode, "mode", "preflight", "preflight, drain or rollback")
	f.StringVar(&collectorLegacyCutoverFlags.LegacySpace, "legacy-space", "crypto", "legacy collector space")
	f.StringVar(&collectorLegacyCutoverFlags.ControlURL, "control-url", "", "Control service base URL")
}
