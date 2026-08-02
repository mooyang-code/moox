// Package clsreporter provides framework-free, bounded CLS batch reporting.
package clsreporter

import (
	"context"
	"fmt"
	"sync"
	"time"

	cls "github.com/tencentcloud/tencentcloud-cls-sdk-go"
)

const maxEntries = 10000

// Entry is a single structured log record. Fields are copied by Report so it
// is safe to reuse the caller map from concurrent collection workers.
type Entry struct {
	Timestamp time.Time
	Fields    map[string]string
}

// Reporter accepts records during an invocation and sends at most one CLS
// request when Flush is called.
type Reporter interface {
	Report(Entry)
	Flush(context.Context) error
}

type sender interface {
	SendLogList(context.Context, string, []*cls.Log) error
}

type reportClient struct {
	mu      sync.Mutex
	topicID string
	sender  sender
	entries []Entry
}

// New creates a synchronous CLS reporter. It creates no background workers,
// which keeps short-lived SCF invocations bounded.
func New(cfg Config) (Reporter, error) {
	if cfg.Endpoint == "" || cfg.TopicID == "" || cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("CLS endpoint, topic id, secret id, and secret key are required")
	}
	if cfg.Timeout < minTimeout || cfg.Timeout > maxTimeout {
		return nil, fmt.Errorf("CLS timeout must be between %s and %s", minTimeout, maxTimeout)
	}
	producerCfg := cls.GetDefaultSyncProducerClientConfig()
	producerCfg.Endpoint = cfg.Endpoint
	producerCfg.AccessKeyID = cfg.SecretID
	producerCfg.AccessKeySecret = cfg.SecretKey
	producerCfg.Timeout = int(cfg.Timeout / time.Millisecond)
	producerCfg.IdleConn = 1
	producerCfg.NeedSource = false
	producerCfg.HostName = cfg.Source
	client, err := cls.NewSyncProducerClient(producerCfg)
	if err != nil {
		return nil, fmt.Errorf("create CLS producer: %w", err)
	}
	return &reportClient{topicID: cfg.TopicID, sender: client, entries: make([]Entry, 0, 64)}, nil
}

func (r *reportClient) Report(entry Entry) {
	if r == nil || len(entry.Fields) == 0 {
		return
	}
	fields := make(map[string]string, len(entry.Fields))
	for key, value := range entry.Fields {
		fields[key] = value
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	entry.Fields = fields
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) < maxEntries {
		r.entries = append(r.entries, entry)
	}
}

func (r *reportClient) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	entries := append([]Entry(nil), r.entries...)
	r.entries = r.entries[:0]
	r.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	logs := make([]*cls.Log, 0, len(entries))
	for _, entry := range entries {
		logs = append(logs, cls.NewCLSLog(entry.Timestamp.Unix(), entry.Fields))
	}
	return r.sender.SendLogList(ctx, r.topicID, logs)
}

type noopReporter struct{}

// Noop returns a reporter used when direct CLS reporting is disabled.
func Noop() Reporter { return noopReporter{} }

func (noopReporter) Report(Entry)                 {}
func (noopReporter) Flush(context.Context) error { return nil }
