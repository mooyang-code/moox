package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStrategyDefinitionRoundTripUsesDSLNameAndUpdatedAt(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	want := StrategyDefinition{StrategyID: "s1", StrategyName: "momentum", DSLYaml: "name: momentum", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveStrategyDefinition(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStrategyDefinition(context.Background(), want.StrategyID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("definition = %+v, want %+v", got, want)
	}
	defs, err := repo.ListStrategyDefinitions(context.Background(), "moment")
	if err != nil || len(defs) != 1 || defs[0].StrategyName != "momentum" {
		t.Fatalf("ListStrategyDefinitions() = %+v, err=%v", defs, err)
	}
}

func TestStrategyInstanceRoundTripAndEnabledAccountUniqueness(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	account := "acct-1"
	first := StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", InputBindingsJSON: json.RawMessage(`{"view":"v1"}`), LogicalAccountID: &account, CreatedAt: now, UpdatedAt: now}
	second := first
	second.InstanceID = "i2"
	if err := repo.CreateInstance(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetInstance(context.Background(), first.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != first.InstanceID || string(got.InputBindingsJSON) != string(first.InputBindingsJSON) || got.Enabled {
		t.Fatalf("instance = %+v", got)
	}
	if err := repo.CreateInstance(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	firstSession := "session-1"
	if err := repo.SetInstanceEnabled(context.Background(), first.InstanceID, true, &firstSession, time.UnixMilli(2000).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(context.Background(), second.InstanceID, true, &firstSession, time.UnixMilli(2000).UTC()); err == nil {
		t.Fatal("second enabled account was accepted")
	}
}

func TestUpdateStrategyDefinitionRequiresDisabledInstances(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	if err := repo.SaveStrategyDefinition(context.Background(), StrategyDefinition{StrategyID: "s1", StrategyName: "old", DSLYaml: "name: old", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(context.Background(), StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.SetInstanceEnabled(context.Background(), "i1", true, &session, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStrategyDefinition(context.Background(), StrategyDefinition{StrategyID: "s1", StrategyName: "new", DSLYaml: "name: new", UpdatedAt: time.UnixMilli(2000).UTC()}); err == nil {
		t.Fatal("enabled instance did not block definition update")
	}
}

func TestCommitResultStoresFrozenSnapshotAndCancelsOlderPending(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	session := "session-1"
	if err := repo.CreateInstance(context.Background(), StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", InputBindingsJSON: json.RawMessage(`{}`), LogicalAccountID: nil, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(context.Background(), "i1", true, &session, now); err != nil {
		t.Fatal(err)
	}
	commit := func(id string, bar int64, status PublishStatus, event []byte) StrategyResult {
		return StrategyResult{ResultID: id, InstanceID: "i1", SessionID: session, BarEndTime: time.UnixMilli(bar).UTC(), ValidUntil: time.UnixMilli(10000).UTC(), SnapshotJSON: json.RawMessage(`{"strategy_id":"s1","dsl_yaml":"name: s1","inputs":{}}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), EventData: event, PublishStatus: status, CreatedAt: time.UnixMilli(bar + 1).UTC()}
	}
	first := commit("r1", 2000, PublishPending, []byte("event-1"))
	if _, created, err := repo.CommitResult(context.Background(), CommitResultRequest{Result: first, Now: now}); err != nil || !created {
		t.Fatalf("first commit created=%v err=%v", created, err)
	}
	second := commit("r2", 3000, PublishPending, []byte("event-2"))
	if _, created, err := repo.CommitResult(context.Background(), CommitResultRequest{Result: second, ExpectedResultID: ptr("r1"), Now: now}); err != nil || !created {
		t.Fatalf("second commit created=%v err=%v", created, err)
	}
	firstGot, err := repo.GetStrategyResult(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if firstGot.PublishStatus != PublishCancelled {
		t.Fatalf("older pending status = %q", firstGot.PublishStatus)
	}
	secondGot, err := repo.GetStrategyResult(context.Background(), "r2")
	if err != nil {
		t.Fatal(err)
	}
	secondGot.TargetsJSON[0] = 'x'
	reloaded, err := repo.GetStrategyResult(context.Background(), "r2")
	if err != nil {
		t.Fatal(err)
	}
	if string(reloaded.TargetsJSON) != "[]" || !errors.Is(repo.TransitionPublishStatus(context.Background(), "r2", PublishPending, PublishSent), nil) {
		t.Fatalf("result mutation or status transition failed: %+v", reloaded)
	}
}

func TestCommitResultRejectsInvalidObservationEventAndStatusTransition(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	session := "session-1"
	if err := repo.CreateInstance(context.Background(), StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(context.Background(), "i1", true, &session, now); err != nil {
		t.Fatal(err)
	}
	result := StrategyResult{ResultID: "r1", InstanceID: "i1", SessionID: session, BarEndTime: time.UnixMilli(2000), ValidUntil: time.UnixMilli(10000), SnapshotJSON: json.RawMessage(`{}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), EventData: []byte("unexpected"), PublishStatus: PublishNone, CreatedAt: time.UnixMilli(2001)}
	if _, _, err := repo.CommitResult(context.Background(), CommitResultRequest{Result: result, Now: now}); err == nil {
		t.Fatal("observation result with event data was accepted")
	}
}

func TestCommitResultRejectsNullResultJSON(t *testing.T) {
	repo := openCurrentStore(t)
	now := time.UnixMilli(1000).UTC()
	session := "session-1"
	if err := repo.CreateInstance(context.Background(), StrategyInstance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(context.Background(), "i1", true, &session, now); err != nil {
		t.Fatal(err)
	}
	result := StrategyResult{ResultID: "null-result", InstanceID: "i1", SessionID: session, BarEndTime: time.UnixMilli(2000), ValidUntil: time.UnixMilli(10000), SnapshotJSON: json.RawMessage(`null`), TargetsJSON: json.RawMessage(`null`), RuleStatesJSON: json.RawMessage(`null`), PublishStatus: PublishNone, CreatedAt: time.UnixMilli(2001)}
	if _, _, err := repo.CommitResult(context.Background(), CommitResultRequest{Result: result, Now: now}); err == nil {
		t.Fatal("null result JSON should be rejected")
	}
}

func ptr(value string) *string { return &value }
