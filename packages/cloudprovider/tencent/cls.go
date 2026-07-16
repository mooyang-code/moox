package tencent

import (
	"context"
	"fmt"
	"strings"
)

// CLSAPI is the resource-management subset required by MooX initialization.
type CLSAPI interface {
	GetService(context.Context) (bool, error)
	OpenService(context.Context) (string, error)
	FindLogset(context.Context, string) (CLSLogset, bool, error)
	CreateLogset(context.Context, string) (CLSLogset, string, error)
	FindTopic(context.Context, string, string) (CLSTopic, bool, error)
	CreateTopic(context.Context, CLSCreateTopicOptions) (CLSTopic, string, error)
	CreateIndex(context.Context, string) (string, error)
}

type CLSLogset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CLSTopic struct {
	ID           string `json:"id"`
	LogsetID     string `json:"logset_id"`
	Name         string `json:"name"`
	IndexEnabled bool   `json:"index_enabled"`
}

type CLSCreateTopicOptions struct {
	LogsetID      string
	Name          string
	RetentionDays int64
	Partitions    int64
}

type CLSBootstrapOptions struct {
	LogsetName    string
	TopicName     string
	RetentionDays int64
	Partitions    int64
}

type CLSBootstrapResult struct {
	ServiceOpened bool     `json:"service_opened"`
	LogsetCreated bool     `json:"logset_created"`
	TopicCreated  bool     `json:"topic_created"`
	IndexCreated  bool     `json:"index_created"`
	LogsetID      string   `json:"logset_id"`
	TopicID       string   `json:"topic_id"`
	RequestIDs    []string `json:"request_ids,omitempty"`
}

// BootstrapCLS idempotently opens CLS and prepares one indexed log topic.
func BootstrapCLS(ctx context.Context, api CLSAPI, opts CLSBootstrapOptions) (CLSBootstrapResult, error) {
	if api == nil {
		return CLSBootstrapResult{}, fmt.Errorf("cls api is required")
	}
	opts.LogsetName = strings.TrimSpace(opts.LogsetName)
	opts.TopicName = strings.TrimSpace(opts.TopicName)
	if opts.LogsetName == "" || strings.Contains(opts.LogsetName, "|") {
		return CLSBootstrapResult{}, fmt.Errorf("valid logset name is required")
	}
	if opts.TopicName == "" || strings.Contains(opts.TopicName, "|") {
		return CLSBootstrapResult{}, fmt.Errorf("valid topic name is required")
	}
	if opts.RetentionDays < 1 || (opts.RetentionDays > 3600 && opts.RetentionDays != 3640) {
		return CLSBootstrapResult{}, fmt.Errorf("retention days must be 1..3600 or 3640")
	}
	if opts.Partitions < 1 || opts.Partitions > 10 {
		return CLSBootstrapResult{}, fmt.Errorf("partitions must be 1..10")
	}

	var result CLSBootstrapResult
	opened, err := api.GetService(ctx)
	if err != nil {
		return result, fmt.Errorf("query CLS service: %w", err)
	}
	if !opened {
		requestID, err := api.OpenService(ctx)
		if err != nil {
			return result, fmt.Errorf("open CLS service: %w", err)
		}
		result.ServiceOpened = true
		appendRequestID(&result, requestID)
	}

	var requestID string
	logset, found, err := api.FindLogset(ctx, opts.LogsetName)
	if err != nil {
		return result, fmt.Errorf("find CLS logset: %w", err)
	}
	if !found {
		logset, requestID, err = api.CreateLogset(ctx, opts.LogsetName)
		if err != nil {
			return result, fmt.Errorf("create CLS logset: %w", err)
		}
		result.LogsetCreated = true
		appendRequestID(&result, requestID)
	}
	if strings.TrimSpace(logset.ID) == "" {
		return result, fmt.Errorf("CLS logset returned an empty id")
	}
	result.LogsetID = logset.ID

	topic, found, err := api.FindTopic(ctx, logset.ID, opts.TopicName)
	if err != nil {
		return result, fmt.Errorf("find CLS topic: %w", err)
	}
	if !found {
		topic, requestID, err = api.CreateTopic(ctx, CLSCreateTopicOptions{
			LogsetID: logset.ID, Name: opts.TopicName,
			RetentionDays: opts.RetentionDays, Partitions: opts.Partitions,
		})
		if err != nil {
			return result, fmt.Errorf("create CLS topic: %w", err)
		}
		result.TopicCreated = true
		appendRequestID(&result, requestID)
	}
	if strings.TrimSpace(topic.ID) == "" {
		return result, fmt.Errorf("CLS topic returned an empty id")
	}
	result.TopicID = topic.ID
	if !topic.IndexEnabled {
		requestID, err := api.CreateIndex(ctx, topic.ID)
		if err != nil {
			return result, fmt.Errorf("create CLS index: %w", err)
		}
		result.IndexCreated = true
		appendRequestID(&result, requestID)
	}
	return result, nil
}

func appendRequestID(result *CLSBootstrapResult, requestID string) {
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		result.RequestIDs = append(result.RequestIDs, requestID)
	}
}
