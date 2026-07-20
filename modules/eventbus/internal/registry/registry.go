// Package registry reconciles the declarative EventBus topology before readiness.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	nats "github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Registry struct {
	js  nats.JetStreamContext
	cfg *config.Config
}

type Result struct {
	Streams int
	KV      int
	Topics  int
}

func New(js nats.JetStreamContext, cfg *config.Config) (*Registry, error) {
	if js == nil {
		return nil, fmt.Errorf("jetstream context is nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("registry config is nil")
	}
	return &Registry{js: js, cfg: cfg}, nil
}

func (r *Registry) Reconcile(ctx context.Context) (Result, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := r.cfg.Validate(); err != nil {
		return Result{}, err
	}
	for i := range r.cfg.Streams {
		if err := reconcileStream(ctx, r.js, &r.cfg.Streams[i], r.cfg); err != nil {
			return Result{}, err
		}
	}
	if err := r.rejectOverlappingUnmanagedStreams(ctx); err != nil {
		return Result{}, err
	}
	for i := range r.cfg.KV {
		if err := reconcileKV(r.js, &r.cfg.KV[i], r.cfg); err != nil {
			return Result{}, err
		}
	}
	for i := range r.cfg.Consumers {
		if err := reconcileConsumer(ctx, r.js, &r.cfg.Consumers[i]); err != nil {
			return Result{}, err
		}
	}
	return Result{Streams: len(r.cfg.Streams), KV: len(r.cfg.KV), Topics: enabledTopics(r.cfg)}, nil
}

func reconcileConsumer(ctx context.Context, js nats.JetStreamContext, spec *config.ConsumerConfig) error {
	want := consumerConfig(spec)
	info, err := js.ConsumerInfo(spec.Stream, spec.Durable, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		if _, err := js.AddConsumer(spec.Stream, want, nats.Context(ctx)); err != nil {
			return fmt.Errorf("create consumer %q/%q: %w", spec.Stream, spec.Durable, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect consumer %q/%q: %w", spec.Stream, spec.Durable, err)
	}
	if info == nil {
		return fmt.Errorf("consumer %q/%q info is empty", spec.Stream, spec.Durable)
	}
	actual := info.Config
	if actual.FilterSubject != want.FilterSubject || actual.AckPolicy != want.AckPolicy || actual.DeliverPolicy != want.DeliverPolicy || actual.ReplayPolicy != want.ReplayPolicy {
		return fmt.Errorf("consumer %q/%q immutable configuration mismatch", spec.Stream, spec.Durable)
	}
	if actual.AckWait == want.AckWait && actual.MaxAckPending == want.MaxAckPending && actual.MaxDeliver == want.MaxDeliver {
		return nil
	}
	next := actual
	next.AckWait = want.AckWait
	next.MaxAckPending = want.MaxAckPending
	next.MaxDeliver = want.MaxDeliver
	if _, err := js.UpdateConsumer(spec.Stream, &next, nats.Context(ctx)); err != nil {
		return fmt.Errorf("update consumer %q/%q: %w", spec.Stream, spec.Durable, err)
	}
	return nil
}

func consumerConfig(spec *config.ConsumerConfig) *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Name: spec.Durable, Durable: spec.Durable, FilterSubject: spec.FilterSubject,
		AckPolicy: parseAckPolicy(spec.AckPolicy), DeliverPolicy: parseDeliverPolicy(spec.DeliverPolicy),
		ReplayPolicy: parseReplayPolicy(spec.ReplayPolicy), AckWait: spec.AckWait,
		MaxAckPending: spec.MaxAckPending, MaxDeliver: spec.MaxDeliver,
	}
}

func parseAckPolicy(value string) nats.AckPolicy {
	if strings.EqualFold(value, "none") {
		return nats.AckNonePolicy
	}
	if strings.EqualFold(value, "all") {
		return nats.AckAllPolicy
	}
	return nats.AckExplicitPolicy
}

func parseDeliverPolicy(value string) nats.DeliverPolicy {
	if strings.EqualFold(value, "new") {
		return nats.DeliverNewPolicy
	}
	return nats.DeliverAllPolicy
}

func parseReplayPolicy(value string) nats.ReplayPolicy {
	if strings.EqualFold(value, "original") {
		return nats.ReplayOriginalPolicy
	}
	return nats.ReplayInstantPolicy
}

func (r *Registry) rejectOverlappingUnmanagedStreams(ctx context.Context) error {
	configured := make(map[string]struct{}, len(r.cfg.Streams))
	for _, stream := range r.cfg.Streams {
		configured[stream.Name] = struct{}{}
	}
	for name := range r.js.StreamNames(nats.Context(ctx)) {
		if _, ok := configured[name]; ok || strings.HasPrefix(name, "KV_") {
			continue
		}
		info, err := r.js.StreamInfo(name, nats.Context(ctx))
		if err != nil {
			return fmt.Errorf("inspect unmanaged stream %q: %w", name, err)
		}
		for _, unmanaged := range info.Config.Subjects {
			for _, stream := range r.cfg.Streams {
				for _, managed := range stream.Subjects {
					if subjectPatternsOverlap(unmanaged, managed) {
						return fmt.Errorf("unmanaged stream %q overlaps managed stream %q subject %q", name, stream.Name, managed)
					}
				}
			}
		}
	}
	return nil
}

func subjectPatternsOverlap(a, b string) bool {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; ; i++ {
		if i >= len(aa) || i >= len(bb) {
			return i >= len(aa) && i >= len(bb)
		}
		if aa[i] == ">" || bb[i] == ">" {
			return true
		}
		if aa[i] != "*" && bb[i] != "*" && aa[i] != bb[i] {
			return false
		}
	}
}

func (r *Registry) JS() nats.JetStreamContext {
	if r == nil {
		return nil
	}
	return r.js
}
func (r *Registry) Config() *config.Config {
	if r == nil {
		return nil
	}
	return r.cfg
}

func enabledTopics(c *config.Config) int {
	n := 0
	for _, t := range c.Topics {
		if t.Enabled {
			n++
		}
	}
	return n
}

func streamConfig(s *config.StreamConfig) *nats.StreamConfig {
	retention := nats.LimitsPolicy
	if s.Retention == "work_queue" {
		retention = nats.WorkQueuePolicy
	}
	storage := nats.FileStorage
	if s.Storage == "memory" {
		storage = nats.MemoryStorage
	}
	discard := nats.DiscardOld
	if strings.EqualFold(s.Discard, "new") {
		discard = nats.DiscardNew
	}
	return &nats.StreamConfig{Name: s.Name, Description: s.Description, Subjects: append([]string(nil), s.Subjects...), Retention: retention, Storage: storage, Replicas: s.Replicas, MaxAge: s.MaxAge, MaxBytes: s.MaxBytes, MaxMsgs: s.MaxMsgs, Discard: discard, Duplicates: 2 * time.Minute}
}

func reconcileStream(ctx context.Context, js nats.JetStreamContext, spec *config.StreamConfig, cfg *config.Config) error {
	if spec.Replicas > 1 && (!cfg.Broker.Cluster.Enabled || spec.Replicas > len(cfg.Broker.Cluster.Routes)+1) {
		return fmt.Errorf("stream %q replicas=%d exceed reachable cluster size", spec.Name, spec.Replicas)
	}
	want := streamConfig(spec)
	got, err := js.StreamInfo(spec.Name, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(want, nats.Context(ctx))
		if err != nil {
			return fmt.Errorf("create stream %q: %w", spec.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stream %q: %w", spec.Name, err)
	}
	if got == nil {
		return fmt.Errorf("stream %q info is empty", spec.Name)
	}
	if got.Config.Retention != want.Retention {
		return fmt.Errorf("stream %q retention change is forbidden", spec.Name)
	}
	if got.Config.Storage != want.Storage {
		return fmt.Errorf("stream %q storage change is forbidden", spec.Name)
	}
	if subjectRemoved(got.Config.Subjects, want.Subjects) && got.State.Msgs > 0 {
		return fmt.Errorf("stream %q subject removal with stored messages is forbidden", spec.Name)
	}
	if equalStreamConfig(&got.Config, want) {
		return nil
	}
	if _, err := js.UpdateStream(want, nats.Context(ctx)); err != nil {
		return fmt.Errorf("update stream %q: %w", spec.Name, err)
	}
	return nil
}

func reconcileKV(js nats.JetStreamContext, spec *config.KVConfig, cfg *config.Config) error {
	if spec.Replicas > 1 && (!cfg.Broker.Cluster.Enabled || spec.Replicas > len(cfg.Broker.Cluster.Routes)+1) {
		return fmt.Errorf("kv %q replicas=%d exceed reachable cluster size", spec.Bucket, spec.Replicas)
	}
	want := &nats.KeyValueConfig{Bucket: spec.Bucket, History: uint8(spec.History), TTL: spec.MaxAge, Storage: storageType(spec.Storage), Replicas: spec.Replicas, Description: spec.Description}
	if _, err := js.KeyValue(spec.Bucket); errors.Is(err, nats.ErrBucketNotFound) {
		if _, err := js.CreateKeyValue(want); err != nil {
			return fmt.Errorf("create kv %q: %w", spec.Bucket, err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect kv %q: %w", spec.Bucket, err)
	}
	// The legacy KV API does not expose an atomic update operation. Inspect the
	// backing stream and update only fields that are safe to reconcile.
	streamName := "KV_" + spec.Bucket
	info, err := js.StreamInfo(streamName)
	if err != nil {
		return fmt.Errorf("inspect kv backing stream %q: %w", spec.Bucket, err)
	}
	if info.Config.Retention != nats.LimitsPolicy || info.Config.MaxMsgsPerSubject < 1 {
		return fmt.Errorf("kv %q backing stream is invalid", spec.Bucket)
	}
	if info.Config.Storage != storageType(spec.Storage) {
		return fmt.Errorf("kv %q storage change is forbidden", spec.Bucket)
	}
	if info.Config.MaxMsgsPerSubject != int64(spec.History) {
		return fmt.Errorf("kv %q history change is forbidden", spec.Bucket)
	}
	if info.Config.MaxAge != spec.MaxAge || info.Config.Replicas != spec.Replicas || info.Config.Description != spec.Description {
		if _, err := js.UpdateStream(&nats.StreamConfig{Name: info.Config.Name, Subjects: info.Config.Subjects, Retention: info.Config.Retention, Storage: info.Config.Storage, Replicas: info.Config.Replicas, MaxAge: spec.MaxAge, MaxMsgsPerSubject: int64(spec.History), MaxBytes: info.Config.MaxBytes, MaxMsgs: info.Config.MaxMsgs, Discard: info.Config.Discard, DenyDelete: info.Config.DenyDelete, DenyPurge: info.Config.DenyPurge, AllowRollup: info.Config.AllowRollup, Description: spec.Description}); err != nil {
			return fmt.Errorf("update kv %q: %w", spec.Bucket, err)
		}
	}
	return nil
}

func storageType(value string) nats.StorageType {
	if value == "memory" {
		return nats.MemoryStorage
	}
	return nats.FileStorage
}

func equalStreamConfig(a, b *nats.StreamConfig) bool {
	if a.Name != b.Name || a.Description != b.Description || a.Retention != b.Retention || a.Storage != b.Storage || a.Replicas != b.Replicas || a.MaxAge != b.MaxAge || a.MaxBytes != b.MaxBytes || a.MaxMsgs != b.MaxMsgs || a.Discard != b.Discard || a.Duplicates != b.Duplicates {
		return false
	}
	return sameStrings(a.Subjects, b.Subjects)
}
func sameStrings(a, b []string) bool {
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
func subjectRemoved(old, next []string) bool {
	for _, value := range old {
		found := false
		for _, candidate := range next {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func TopicStream(cfg *config.Config, topic string) (config.TopicConfig, string, error) {
	for _, t := range cfg.Topics {
		if t.Enabled && t.Topic == topic {
			return t, t.Stream, nil
		}
	}
	for _, family := range cfg.TopicFamilies {
		if family.Enabled && topicMatchesPattern(topic, family.Pattern) {
			return config.TopicConfig{Topic: topic, Stream: family.Stream, Kind: family.Kind, PayloadContentType: family.PayloadContentType, PayloadVersion: family.PayloadVersion, Enabled: family.Enabled}, family.Stream, nil
		}
	}
	return config.TopicConfig{}, "", fmt.Errorf("topic %q is not registered", topic)
}

func topicMatchesPattern(topic, pattern string) bool {
	a, b := strings.Split(topic, "."), strings.Split(pattern, ".")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if b[i] != "*" && b[i] != a[i] {
			return false
		}
	}
	return true
}

func (r *Registry) ValidateTopic(topic string) error {
	_, _, err := TopicStream(r.cfg, strings.TrimSpace(topic))
	return err
}
