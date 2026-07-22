package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	activationAppID   = "storage-metadata"
	activationTimeout = 3 * time.Second
)

var activationCheckIDs = []string{
	"dataset_state",
	"dataset_schema",
	"keep_duration",
	"data_node",
	"service_target",
	"data_node_readiness",
	"data_node_identity",
}

// NodeStateChecker is the only network boundary used by Dataset activation.
// The target is passed explicitly so the checker cannot silently fall back to
// an endpoint or a cached route.
type NodeStateChecker interface {
	GetNodeState(context.Context, string, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error)
}

type rpcNodeStateChecker struct{}

func (rpcNodeStateChecker) GetNodeState(ctx context.Context, target string, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	if _, err := parseIPTarget(target); err != nil {
		return nil, err
	}
	proxy := pb.NewDataNodeRuntimeClientProxy(
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
	)
	return proxy.GetNodeState(ctx, req)
}

type activationChecker struct {
	reader  metadata.Reader
	node    NodeStateChecker
	secret  string
	timeout time.Duration
}

func newActivationChecker(reader metadata.Reader, node NodeStateChecker, secret string) *activationChecker {
	if node == nil {
		node = rpcNodeStateChecker{}
	}
	return &activationChecker{reader: reader, node: node, secret: secret, timeout: activationTimeout}
}

func (c *activationChecker) checks(ctx context.Context, dataset *pb.Dataset) []*pb.DatasetActivationCheck {
	checks := make([]*pb.DatasetActivationCheck, 0, len(activationCheckIDs))
	add := func(id string, ready bool, summary string) {
		checks = append(checks, &pb.DatasetActivationCheck{CheckId: id, Ready: ready, Summary: summary})
	}

	if dataset == nil {
		add("dataset_state", false, "Dataset is missing")
		return checks
	}
	stateReady := datasetActivationStateReady(dataset)
	if stateReady {
		add("dataset_state", true, "Dataset state is eligible for activation")
	} else {
		add("dataset_state", false, "Dataset state is not eligible for activation")
	}

	schemaErr := validateDatasetActivationSchema(dataset)
	add("dataset_schema", schemaErr == nil, activationSummary("Dataset schema", schemaErr))
	keepErr := validateDatasetActivationKeepDuration(dataset)
	add("keep_duration", keepErr == nil, activationSummary("keep_duration", keepErr))

	var node *pb.DataNode
	if c.reader == nil {
		add("data_node", false, "DataNode metadata is unavailable")
	} else {
		loaded, err := c.reader.GetDataNode(ctx, dataset.GetDataNodeId())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			add("data_node", false, "DataNode metadata could not be read")
		} else if loaded == nil {
			add("data_node", false, "DataNode does not exist")
		} else if loaded.GetStatus() != "active" {
			node = loaded
			add("data_node", false, "DataNode is disabled")
		} else {
			node = loaded
			add("data_node", true, "DataNode is active")
		}
	}

	target, targetErr := "", errors.New("DataNode is unavailable")
	if node != nil && node.GetStatus() == "active" {
		target, targetErr = parseIPTarget(node.GetServiceTarget())
	}
	add("service_target", targetErr == nil, activationSummary("service_target", targetErr))

	var state *pb.GetNodeStateRsp
	var stateErr error
	if targetErr == nil {
		checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
		state, stateErr = c.node.GetNodeState(checkCtx, target, &pb.GetNodeStateReq{
			NodeId: dataset.GetDataNodeId(),
			AuthInfo: &pb.AuthInfo{
				AppId:  activationAppID,
				AppKey: datanode.ServiceAuthKey(c.secret, activationAppID),
			},
		})
		cancel()
	}
	readinessReady := stateErr == nil && state != nil && state.GetRetInfo() != nil && state.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && state.GetStatus() == "READY"
	if stateErr != nil || state == nil || state.GetRetInfo() == nil || state.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		add("data_node_readiness", false, "DataNode readiness check failed")
	} else if state.GetStatus() != "READY" {
		add("data_node_readiness", false, "DataNode is not READY")
	} else {
		add("data_node_readiness", true, "DataNode is READY")
	}
	identityReady := readinessReady && state.GetNodeId() == dataset.GetDataNodeId()
	if !readinessReady {
		add("data_node_identity", false, "DataNode identity was not verified")
	} else if !identityReady {
		add("data_node_identity", false, "DataNode identity does not match metadata")
	} else {
		add("data_node_identity", true, "DataNode identity matches metadata")
	}

	return checks
}

func activationSummary(name string, err error) string {
	if err == nil {
		return name + " is valid"
	}
	return name + " is invalid"
}

func activationReady(checks []*pb.DatasetActivationCheck) bool {
	if len(checks) != len(activationCheckIDs) {
		return false
	}
	for i, check := range checks {
		if check == nil || check.GetCheckId() != activationCheckIDs[i] || !check.GetReady() {
			return false
		}
	}
	return true
}

func datasetActivationStateReady(dataset *pb.Dataset) bool {
	switch {
	case dataset.GetStatus() == "disabled":
		return true
	case dataset.GetStatus() == "active" && dataset.GetBindingLocked():
		return true
	default:
		return false
	}
}

func validateDatasetActivationSchema(dataset *pb.Dataset) error {
	if strings.TrimSpace(dataset.GetSpaceId()) == "" || strings.TrimSpace(dataset.GetDataSourceId()) == "" || strings.TrimSpace(dataset.GetName()) == "" {
		return errors.New("required Dataset fields are missing")
	}
	if err := validateDatasetID(dataset.GetDatasetId()); err != nil {
		return err
	}
	switch dataset.GetDataKind() {
	case pb.DataKind_DATA_KIND_RECORD, pb.DataKind_DATA_KIND_TIME_SERIES:
		return nil
	default:
		return errors.New("Dataset data_kind is unsupported")
	}
}

func validateDatasetActivationKeepDuration(dataset *pb.Dataset) error {
	value := strings.TrimSpace(dataset.GetKeepDuration())
	if value == "" || value == "0" {
		return nil
	}
	if dataset.GetDataKind() == pb.DataKind_DATA_KIND_RECORD {
		return errors.New("record Dataset keep_duration must be 0")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return errors.New("keep_duration must be 0 or a positive duration")
	}
	return nil
}

func parseIPTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ip" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("service_target must be an ip://host:port address")
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return "", errors.New("service_target must include host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("service_target port is invalid")
	}
	return "ip://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}
