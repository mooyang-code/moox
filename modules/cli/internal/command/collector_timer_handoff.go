package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
)

const collectorPublishLeaseHandoffRetryInterval = time.Second

type collectorTimerAssignment struct {
	NodeID          string
	PackageID       string
	AssignmentHash  string
	BindingHash     string
	AssignmentCount int
}

func collectorTimerAssignmentFromNode(node adminclient.CloudNode, expectedPackageID string) (collectorTimerAssignment, bool) {
	if strings.TrimSpace(node.NodeID) == "" || strings.TrimSpace(expectedPackageID) == "" || node.PackageID != expectedPackageID {
		return collectorTimerAssignment{}, false
	}
	assignmentHash := metadataStringValue(node.Metadata, "assignment_hash")
	bindingHash := metadataStringValue(node.Metadata, "binding_hash")
	count, hasCount := metadataIntValue(node.Metadata, "assignment_count")
	reconciledAt := metadataStringValue(node.Metadata, "runtime_config_reconciled_at")
	if assignmentHash == "" || bindingHash == "" || !hasCount || count <= 0 || reconciledAt == "" {
		return collectorTimerAssignment{}, false
	}
	return collectorTimerAssignment{
		NodeID: node.NodeID, PackageID: node.PackageID, AssignmentHash: assignmentHash,
		BindingHash: bindingHash, AssignmentCount: count,
	}, true
}

func collectorTimerAssignmentsMatch(expected, actual map[string]collectorTimerAssignment) bool {
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	for nodeID, assignment := range expected {
		if actual[nodeID] != assignment {
			return false
		}
	}
	return true
}

func isCollectorPublishLeaseHeld(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "collector publish lease is held") || strings.Contains(message, "lease held")
}

func reacquireCollectorPublishLeaseAfterHandoff(ctx context.Context, client *adminclient.Client, spaceID string) (context.Context, *collectorPublishLeaseGuard, error) {
	waitCtx, cancel := context.WithTimeout(ctx, collectorStockCNFleetWaitTimeout)
	defer cancel()
	for {
		leaseCtx, guard, err := acquireCollectorPublishLeaseWithRequestContext(ctx, waitCtx, client, spaceID)
		if err == nil {
			return leaseCtx, guard, nil
		}
		if !isCollectorPublishLeaseHeld(err) {
			return nil, nil, fmt.Errorf("%w: reacquire Collector publish lease after Timer assignment: %v", errCollectorPublishFenceChanged, err)
		}
		timer := time.NewTimer(collectorPublishLeaseHandoffRetryInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return nil, nil, fmt.Errorf("%w: wait to reacquire Collector publish lease: %v", errCollectorPublishFenceChanged, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func handoffCollectorPublishLeaseForTimerAssignment(
	baseCtx context.Context,
	client *adminclient.Client,
	spaceID string,
	leaseGuard **collectorPublishLeaseGuard,
	fleets []collectorPublishedTimerFleet,
) (context.Context, map[string]collectorTimerAssignment, error) {
	if leaseGuard == nil || *leaseGuard == nil {
		return nil, nil, fmt.Errorf("%w: Collector publish lease is not held before Timer assignment handoff", errCollectorPublishFenceChanged)
	}
	if err := (*leaseGuard).Close(); err != nil {
		*leaseGuard = nil
		return nil, nil, fmt.Errorf("%w: release Collector publish lease before Timer assignment: %v", errCollectorPublishFenceChanged, err)
	}
	*leaseGuard = nil

	assignmentSnapshot, waitErr := waitCollectorTimerFleetsAssigned(baseCtx, client, fleets)
	leaseCtx, guard, leaseErr := reacquireCollectorPublishLeaseAfterHandoff(baseCtx, client, spaceID)
	if leaseErr != nil {
		return nil, nil, leaseErr
	}
	*leaseGuard = guard

	if err := verifyCollectorTimerFleetPackages(leaseCtx, client, fleets); err != nil {
		return leaseCtx, nil, fmt.Errorf("%w: %v", errCollectorPublishFenceChanged, err)
	}
	if waitErr != nil {
		return leaseCtx, nil, waitErr
	}
	actual, err := inspectCollectorTimerAssignments(leaseCtx, client, fleets)
	if err != nil {
		return leaseCtx, nil, fmt.Errorf("%w: re-read Timer assignment after lease reacquisition: %v", errCollectorPublishFenceChanged, err)
	}
	if !collectorTimerAssignmentsMatch(assignmentSnapshot, actual) {
		return leaseCtx, nil, fmt.Errorf("%w: Timer assignment changed while the publish lease was handed to the reconciler", errCollectorPublishFenceChanged)
	}
	return leaseCtx, assignmentSnapshot, nil
}

func verifyCollectorTimerFleetPackages(ctx context.Context, client *adminclient.Client, fleets []collectorPublishedTimerFleet) error {
	for _, fleet := range fleets {
		nodes, err := inspectCollectorFleet(ctx, client, fleet.opts)
		if err != nil {
			return err
		}
		if len(nodes) != fleet.opts.NodeCount {
			return fmt.Errorf("Timer fleet in %s/%s has %d nodes; expected %d", fleet.opts.Region, fleet.opts.Namespace, len(nodes), fleet.opts.NodeCount)
		}
		if strings.TrimSpace(fleet.packageID) == "" {
			return fmt.Errorf("Timer fleet in %s/%s has no immutable package identity", fleet.opts.Region, fleet.opts.Namespace)
		}
		for _, node := range nodes {
			if node.PackageID != fleet.packageID {
				return fmt.Errorf("Timer node %s package changed from %s to %s", node.NodeID, fleet.packageID, node.PackageID)
			}
		}
	}
	return nil
}

func inspectCollectorTimerAssignments(ctx context.Context, client *adminclient.Client, fleets []collectorPublishedTimerFleet) (map[string]collectorTimerAssignment, error) {
	assignments := make(map[string]collectorTimerAssignment)
	for _, fleet := range fleets {
		nodes, err := inspectCollectorFleet(ctx, client, fleet.opts)
		if err != nil {
			return nil, err
		}
		if len(nodes) != fleet.opts.NodeCount {
			return nil, fmt.Errorf("Timer fleet in %s/%s has %d nodes; expected %d", fleet.opts.Region, fleet.opts.Namespace, len(nodes), fleet.opts.NodeCount)
		}
		for _, node := range nodes {
			assignment, ready := collectorTimerAssignmentFromNode(node, fleet.packageID)
			if !ready {
				return nil, fmt.Errorf("Timer node %s assignment readback is incomplete or does not match package %s", node.NodeID, fleet.packageID)
			}
			if _, exists := assignments[assignment.NodeID]; exists {
				return nil, fmt.Errorf("timer node %s appears in multiple published fleets", assignment.NodeID)
			}
			assignments[assignment.NodeID] = assignment
		}
	}
	return assignments, nil
}
