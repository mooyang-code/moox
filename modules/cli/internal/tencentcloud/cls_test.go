package tencentcloud

import (
	"context"
	"testing"
)

type fakeCLSAPI struct {
	serviceOpen bool
	logset      CLSLogset
	topic       CLSTopic
	calls       []string
}

func (f *fakeCLSAPI) GetService(context.Context) (bool, error) {
	f.calls = append(f.calls, "get-service")
	return f.serviceOpen, nil
}

func (f *fakeCLSAPI) OpenService(context.Context) (string, error) {
	f.calls = append(f.calls, "open-service")
	f.serviceOpen = true
	return "request-open", nil
}

func (f *fakeCLSAPI) FindLogset(_ context.Context, name string) (CLSLogset, bool, error) {
	f.calls = append(f.calls, "find-logset:"+name)
	return f.logset, f.logset.ID != "", nil
}

func (f *fakeCLSAPI) CreateLogset(_ context.Context, name string) (CLSLogset, string, error) {
	f.calls = append(f.calls, "create-logset:"+name)
	f.logset = CLSLogset{ID: "logset-1", Name: name}
	return f.logset, "request-logset", nil
}

func (f *fakeCLSAPI) FindTopic(_ context.Context, logsetID, name string) (CLSTopic, bool, error) {
	f.calls = append(f.calls, "find-topic:"+logsetID+":"+name)
	return f.topic, f.topic.ID != "", nil
}

func (f *fakeCLSAPI) CreateTopic(_ context.Context, opts CLSCreateTopicOptions) (CLSTopic, string, error) {
	f.calls = append(f.calls, "create-topic:"+opts.Name)
	f.topic = CLSTopic{ID: "topic-1", LogsetID: opts.LogsetID, Name: opts.Name}
	return f.topic, "request-topic", nil
}

func (f *fakeCLSAPI) CreateIndex(_ context.Context, topicID string) (string, error) {
	f.calls = append(f.calls, "create-index:"+topicID)
	f.topic.IndexEnabled = true
	return "request-index", nil
}

func TestBootstrapCLSOpensAndCreatesMissingResources(t *testing.T) {
	api := &fakeCLSAPI{}
	result, err := BootstrapCLS(context.Background(), api, CLSBootstrapOptions{
		LogsetName:    "moox",
		TopicName:     "moox-application",
		RetentionDays: 30,
		Partitions:    1,
	})
	if err != nil {
		t.Fatalf("BootstrapCLS() error = %v", err)
	}
	if !result.ServiceOpened || !result.LogsetCreated || !result.TopicCreated || !result.IndexCreated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.LogsetID != "logset-1" || result.TopicID != "topic-1" {
		t.Fatalf("unexpected IDs: %+v", result)
	}
	want := []string{
		"get-service", "open-service", "find-logset:moox", "create-logset:moox",
		"find-topic:logset-1:moox-application", "create-topic:moox-application", "create-index:topic-1",
	}
	if len(api.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", api.calls, want)
	}
	for i := range want {
		if api.calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q", i, api.calls[i], want[i])
		}
	}
}

func TestBootstrapCLSReusesExistingResources(t *testing.T) {
	api := &fakeCLSAPI{
		serviceOpen: true,
		logset:      CLSLogset{ID: "logset-existing", Name: "moox"},
		topic:       CLSTopic{ID: "topic-existing", LogsetID: "logset-existing", Name: "moox-application", IndexEnabled: true},
	}
	result, err := BootstrapCLS(context.Background(), api, CLSBootstrapOptions{
		LogsetName: "moox", TopicName: "moox-application", RetentionDays: 30, Partitions: 1,
	})
	if err != nil {
		t.Fatalf("BootstrapCLS() error = %v", err)
	}
	if result.ServiceOpened || result.LogsetCreated || result.TopicCreated || result.IndexCreated {
		t.Fatalf("existing resources were changed: %+v", result)
	}
	if len(api.calls) != 3 {
		t.Fatalf("calls = %v", api.calls)
	}
}

func TestBootstrapCLSRejectsUnsafeResourceSettings(t *testing.T) {
	for name, opts := range map[string]CLSBootstrapOptions{
		"empty logset": {TopicName: "topic", RetentionDays: 30, Partitions: 1},
		"empty topic":  {LogsetName: "logset", RetentionDays: 30, Partitions: 1},
		"retention":    {LogsetName: "logset", TopicName: "topic", RetentionDays: 0, Partitions: 1},
		"partitions":   {LogsetName: "logset", TopicName: "topic", RetentionDays: 30, Partitions: 11},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BootstrapCLS(context.Background(), &fakeCLSAPI{}, opts); err == nil {
				t.Fatal("BootstrapCLS() error = nil")
			}
		})
	}
}
