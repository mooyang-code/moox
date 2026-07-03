// Package cloudruntime contains the generic CloudNode SCF runtime loop shared by workload modules.
package cloudruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultHTTPTimeout   = 8 * time.Second
	defaultWorkItemLimit = 8

	workItemStatusSuccess = 4
	workItemStatusFailed  = 5
)

// Config describes the MooX control plane and node capabilities for an SCF runtime.
type Config struct {
	ServerIP           string
	ServerPort         int
	SpaceID            string
	NodeID             string
	SupportedWorkloads []string
	Limit              int
	Auth               AuthConfig
	HTTPTimeout        time.Duration
}

// WorkItemLease is the generic CloudNode async work item lease protocol.
type WorkItemLease struct {
	WorkItemID    string `json:"work_item_id"`
	OwnerService  string `json:"owner_service"`
	OwnerRef      string `json:"owner_ref"`
	WorkloadType  string `json:"workload_type"`
	DeploymentID  string `json:"deployment_id"`
	Payload       string `json:"payload"`
	Priority      int    `json:"priority"`
	AttemptNo     int    `json:"attempt_no"`
	LeaseDeadline string `json:"lease_deadline"`
}

// Handler executes one leased CloudNode work item and returns a serialized result.
type Handler func(context.Context, WorkItemLease) (string, error)

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type pollWorkItemsRequest struct {
	SpaceID            string   `json:"space_id"`
	NodeID             string   `json:"node_id"`
	SupportedWorkloads []string `json:"supported_workloads"`
	Limit              int      `json:"limit"`
}

type pollWorkItemLease struct {
	WorkItemID    string         `json:"work_item_id"`
	OwnerService  string         `json:"owner_service"`
	OwnerRef      string         `json:"owner_ref"`
	WorkloadType  string         `json:"workload_type"`
	DeploymentID  string         `json:"deployment_id"`
	Payload       map[string]any `json:"payload"`
	Priority      int            `json:"priority"`
	AttemptNo     int            `json:"attempt_no"`
	LeaseDeadline string         `json:"lease_deadline"`
}

type pollWorkItemsResponse struct {
	RetInfo   *retInfo            `json:"ret_info"`
	WorkItems []pollWorkItemLease `json:"work_items"`
}

type reportWorkItemStatusRequest struct {
	SpaceID      string         `json:"space_id"`
	WorkItemID   string         `json:"work_item_id"`
	NodeID       string         `json:"node_id"`
	Status       int            `json:"status"`
	Result       map[string]any `json:"result"`
	ErrorMessage string         `json:"error_message"`
	DurationMS   int64          `json:"duration_ms"`
}

// PollAndExecuteWorkItems leases CloudNode work items, executes them through handler, and reports final status.
func PollAndExecuteWorkItems(ctx context.Context, cfg Config, handler Handler) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("cloud runtime handler is required")
	}
	workItems, err := pollWorkItems(ctx, cfg)
	if err != nil {
		return err
	}
	if len(workItems) == 0 {
		log.DebugContextf(ctx, "[CloudRuntime] no cloud work items leased")
		return nil
	}
	for _, item := range workItems {
		executeCloudWorkItem(ctx, cfg, handler, item)
	}
	return nil
}

func (cfg *Config) validate() error {
	cfg.ServerIP = strings.TrimSpace(cfg.ServerIP)
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	cfg.NodeID = strings.TrimSpace(cfg.NodeID)
	if cfg.ServerIP == "" || cfg.ServerPort <= 0 || cfg.SpaceID == "" || cfg.NodeID == "" {
		return fmt.Errorf("cloud runtime requires server_ip, server_port, space_id and node_id")
	}
	if len(cfg.SupportedWorkloads) == 0 {
		return fmt.Errorf("cloud runtime supported_workloads is required")
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultWorkItemLimit
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	return nil
}

func pollWorkItems(ctx context.Context, cfg Config) ([]WorkItemLease, error) {
	reqBody := pollWorkItemsRequest{
		SpaceID:            cfg.SpaceID,
		NodeID:             cfg.NodeID,
		SupportedWorkloads: cfg.SupportedWorkloads,
		Limit:              cfg.Limit,
	}
	var rsp pollWorkItemsResponse
	if err := postService(ctx, cfg, "cloudnode", "PollWorkItems", reqBody, &rsp); err != nil {
		return nil, err
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != 0 {
		msg := ""
		if rsp.RetInfo != nil {
			msg = rsp.RetInfo.Msg
		}
		return nil, fmt.Errorf("poll work items failed: %s", msg)
	}
	out := make([]WorkItemLease, 0, len(rsp.WorkItems))
	for _, item := range rsp.WorkItems {
		payload := "{}"
		if len(item.Payload) > 0 {
			raw, err := json.Marshal(item.Payload)
			if err == nil {
				payload = string(raw)
			}
		}
		out = append(out, WorkItemLease{
			WorkItemID:    item.WorkItemID,
			OwnerService:  item.OwnerService,
			OwnerRef:      item.OwnerRef,
			WorkloadType:  item.WorkloadType,
			DeploymentID:  item.DeploymentID,
			Payload:       payload,
			Priority:      item.Priority,
			AttemptNo:     item.AttemptNo,
			LeaseDeadline: item.LeaseDeadline,
		})
	}
	return out, nil
}

func executeCloudWorkItem(ctx context.Context, cfg Config, handler Handler, item WorkItemLease) {
	start := time.Now()
	result, err := handler(ctx, item)
	duration := time.Since(start)
	if err != nil {
		reportCloudWorkItem(ctx, cfg, item.WorkItemID, workItemStatusFailed, result, err.Error(), duration)
		return
	}
	log.InfoContextf(ctx, "[CloudRuntime] cloud work item success work_item_id=%s owner_ref=%s duration=%s", item.WorkItemID, item.OwnerRef, duration)
	reportCloudWorkItem(ctx, cfg, item.WorkItemID, workItemStatusSuccess, result, "", duration)
}

func reportCloudWorkItem(ctx context.Context, cfg Config, workItemID string, status int, result string, errorMessage string, duration time.Duration) {
	resultObj := map[string]any{}
	if strings.TrimSpace(result) != "" {
		_ = json.Unmarshal([]byte(result), &resultObj)
	}
	reqBody := reportWorkItemStatusRequest{
		SpaceID:      cfg.SpaceID,
		WorkItemID:   workItemID,
		NodeID:       cfg.NodeID,
		Status:       status,
		Result:       resultObj,
		ErrorMessage: errorMessage,
		DurationMS:   duration.Milliseconds(),
	}
	var rsp struct {
		RetInfo *retInfo `json:"ret_info"`
	}
	if err := postService(ctx, cfg, "cloudnode", "ReportWorkItemStatus", reqBody, &rsp); err != nil {
		log.WarnContextf(ctx, "[CloudRuntime] report cloud work item failed work_item_id=%s err=%v", workItemID, err)
		return
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != 0 {
		msg := ""
		if rsp.RetInfo != nil {
			msg = rsp.RetInfo.Msg
		}
		log.WarnContextf(ctx, "[CloudRuntime] report cloud work item rejected work_item_id=%s msg=%s", workItemID, msg)
	}
}

func postService(ctx context.Context, cfg Config, module string, method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s:%d/api/service/%s/%s", cfg.ServerIP, cfg.ServerPort, module, method)
	req, err := newSignedRequestWithContext(ctx, http.MethodPost, url, raw, cfg.Auth)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request %s failed status=%d body=%s", url, resp.StatusCode, string(respBody))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}
