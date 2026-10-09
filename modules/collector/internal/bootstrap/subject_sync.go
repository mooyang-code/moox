package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	subjectTagsTimerService       = "trpc.moox.collector.subject_tags.timer"
	subjectAttributesTimerService = "trpc.moox.collector.subject_attributes.timer"
	// subjectSyncTimeout 是一次标签或属性同步的上限；超过一分钟时，下一次触发会被跳过。
	subjectSyncTimeout = 10 * time.Minute
)

// subjectSyncStore 是标的同步需要的 Storage Metadata 接口。
type subjectSyncStore interface {
	subjectsync.TagStore
	subjectsync.AttributeStore
	RegisterSubjectListing(context.Context, map[string][]string) error
}

// setupSubjectSync 初始化标的同步：向 Storage 登记支持的数据源，并注册标签同步与属性同步两个定时器。访问币安的
// 请求按 egress_proxy.domains 经出口代理发出。
func setupSubjectSync(ctx context.Context, s *server.Server, cfg *Config, storageOptions []client.Option, egressProxy httpclient.EgressDoer) error {
	var binanceHTTP *httpclient.HTTPClient
	if len(cfg.EgressProxy.Domains) > 0 {
		domains, err := egresspb.ParseDomainList(cfg.EgressProxy.Domains)
		if err != nil {
			return fmt.Errorf("egress_proxy.domains: %w", err)
		}
		binanceHTTP = httpclient.NewEgressHTTPClient(domains, egressProxy)
	}
	listers, err := marketwiring.NewSubjectListers(binanceHTTP)
	if err != nil {
		return fmt.Errorf("初始化标的同步的数据源: %w", err)
	}
	storage, err := subjectsync.NewStorageClient(storageOptions)
	if err != nil {
		return fmt.Errorf("初始化标的同步的 Storage 客户端: %w", err)
	}
	return registerSubjectSync(ctx, s, cfg.SubjectSync, storage, listers, subjectsync.NewMetrics(prometheus.DefaultRegisterer))
}

func registerSubjectSync(ctx context.Context, s *server.Server, cfg SubjectSyncConfig, storage subjectSyncStore, listers subjectsync.Listers, metrics *subjectsync.Metrics) error {
	if err := storage.RegisterSubjectListing(ctx, listers.Supported()); err != nil {
		return fmt.Errorf("向 Storage 登记标的同步支持的数据源: %w", err)
	}
	tags := &subjectsync.TagRunner{Store: storage, Listers: listers, FetchTimeout: cfg.FetchTimeout, Metrics: metrics}
	attributes := &subjectsync.AttributeRunner{Store: storage, Listers: listers, Jobs: cfg.Attributes, FetchTimeout: cfg.FetchTimeout, Metrics: metrics}
	tagJob, attributeJob, err := subjectSyncJobs(tags, attributes, time.Now)
	if err != nil {
		return err
	}
	for name, job := range map[string]*timerjob.Job{subjectTagsTimerService: tagJob, subjectAttributesTimerService: attributeJob} {
		service := s.Service(name)
		if service == nil {
			return fmt.Errorf("trpc_go.yaml 缺少定时器 %s", name)
		}
		timer.RegisterHandlerService(service, job.Handle)
	}
	return nil
}

// subjectSyncJobs 用 timerjob 包装两个同步，获得超时控制、重叠时跳过和指标上报。
func subjectSyncJobs(tags *subjectsync.TagRunner, attributes *subjectsync.AttributeRunner, now func() time.Time) (*timerjob.Job, *timerjob.Job, error) {
	tagJob, err := timerjob.New("collector_subject_tags", subjectSyncTimeout, func(ctx context.Context) error {
		return tags.RunDue(ctx, now())
	})
	if err != nil {
		return nil, nil, err
	}
	attributeJob, err := timerjob.New("collector_subject_attributes", subjectSyncTimeout, func(ctx context.Context) error {
		return attributes.RunDue(ctx, now())
	})
	if err != nil {
		return nil, nil, err
	}
	return tagJob, attributeJob, nil
}
