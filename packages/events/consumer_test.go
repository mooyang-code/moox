package events

import (
	"strings"
	"testing"
)

func TestSubjectConsumerFilterUsesRegistryIdentity(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{
			Name:  "cloudnode-route",
			Event: CloudJobExecutionRequested,
		},
		SpaceID:   "crypto",
		SubjectID: "collector.pkg/collect.kline",
	}
	got, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.RenderSubject(
		CloudJobExecutionRequested,
		"crypto",
		"collector.pkg/collect.kline",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || strings.Contains(got, ">") {
		t.Fatalf("filter = %q, want exact %q", got, want)
	}
}

func TestSubjectConsumerFilterSeparatesRoutes(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	first, err := subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.symbol",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("distinct routes rendered the same subject %q", first)
	}
}

func TestSubjectConsumerFilterRejectsEmptySpaceID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v, want space_id validation", err)
	}
}

func TestSubjectConsumerFilterRejectsEmptySubjectID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
	})
	if err == nil || !strings.Contains(err.Error(), "subject_id") {
		t.Fatalf("error = %v, want subject_id validation", err)
	}
}

func TestSubjectConsumerFilterRejectsUnregisteredEvent(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: Event{name: "unknown", version: 1}},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %v, want unregistered event validation", err)
	}
}
