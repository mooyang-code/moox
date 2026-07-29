package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

func TestSetRunnerStatusClaimsAndReleasesLogicalAccountOwner(t *testing.T) {
	service, _, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-1")

	enabled, err := service.SetRunnerStatus(context.Background(), &strategypb.SetRunnerStatusReq{
		RunnerId: "runner-1", Status: string(domain.RunnerStatusEnabled),
	})
	if err != nil || enabled.GetRetInfo().GetCode() != 0 {
		t.Fatalf("enable = %+v, %v", enabled, err)
	}
	disabled, err := service.SetRunnerStatus(context.Background(), &strategypb.SetRunnerStatusReq{
		RunnerId: "runner-1", Status: string(domain.RunnerStatusDisabled),
	})
	if err != nil || disabled.GetRetInfo().GetCode() != 0 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	if len(owner.claimed) != 1 || len(owner.released) != 1 {
		t.Fatalf("claimed=%v released=%v", owner.claimed, owner.released)
	}
}

func TestSetRunnerStatusReleasesClaimWhenLocalEnableFails(t *testing.T) {
	service, repo, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-1")
	createRPCRunner(t, service, "runner-2", "strategy-1", "crypto", "logical-1")
	if err := repo.SetRunnerStatus(
		context.Background(), "runner-2", domain.RunnerStatusEnabled, service.Now(),
	); err != nil {
		t.Fatal(err)
	}

	response, err := service.SetRunnerStatus(context.Background(), &strategypb.SetRunnerStatusReq{
		RunnerId: "runner-1", Status: string(domain.RunnerStatusEnabled),
	})
	if err != nil || response.GetRetInfo().GetCode() == 0 {
		t.Fatalf("duplicate enable = %+v, %v", response, err)
	}
	if len(owner.claimed) != 1 || len(owner.released) != 1 {
		t.Fatalf("claim compensation missing: claimed=%v released=%v", owner.claimed, owner.released)
	}
}

func TestSetRunnerStatusRetriesOwnerReleaseAfterLocalDisable(t *testing.T) {
	service, _, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-1")
	enabled, err := service.SetRunnerStatus(
		context.Background(),
		&strategypb.SetRunnerStatusReq{
			RunnerId: "runner-1", Status: string(domain.RunnerStatusEnabled),
		},
	)
	if err != nil || enabled.GetRetInfo().GetCode() != 0 {
		t.Fatalf("enable = %+v, %v", enabled, err)
	}

	owner.releaseErr = context.DeadlineExceeded
	first, err := service.SetRunnerStatus(
		context.Background(),
		&strategypb.SetRunnerStatusReq{
			RunnerId: "runner-1", Status: string(domain.RunnerStatusDisabled),
		},
	)
	if err != nil || first.GetRetInfo().GetCode() == 0 {
		t.Fatalf("first disable = %+v, %v", first, err)
	}
	owner.releaseErr = nil
	retry, err := service.SetRunnerStatus(
		context.Background(),
		&strategypb.SetRunnerStatusReq{
			RunnerId: "runner-1", Status: string(domain.RunnerStatusDisabled),
		},
	)
	if err != nil || retry.GetRetInfo().GetCode() != 0 ||
		len(owner.released) != 2 {
		t.Fatalf("retry disable = %+v, %v released=%v", retry, err, owner.released)
	}
}

func TestSetRunnerStatusReclaimsOwnerWhenRunnerAlreadyEnabled(t *testing.T) {
	service, repo, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-1")
	if err := repo.SetRunnerStatus(
		context.Background(),
		"runner-1",
		domain.RunnerStatusEnabled,
		service.Now(),
	); err != nil {
		t.Fatal(err)
	}

	response, err := service.SetRunnerStatus(
		context.Background(),
		&strategypb.SetRunnerStatusReq{
			RunnerId: "runner-1", Status: string(domain.RunnerStatusEnabled),
		},
	)
	if err != nil || response.GetRetInfo().GetCode() != 0 ||
		len(owner.claimed) != 1 {
		t.Fatalf("response=%+v err=%v claimed=%v", response, err, owner.claimed)
	}
}

func TestUpdateRunnerRequiresDisabledBeforeOwnershipCalls(t *testing.T) {
	service, repo, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-old")
	if err := repo.SetRunnerStatus(
		context.Background(),
		"runner-1",
		domain.RunnerStatusEnabled,
		service.Now(),
	); err != nil {
		t.Fatal(err)
	}

	response, err := service.UpdateRunner(
		context.Background(),
		runnerUpdateRequest("logical-new"),
	)
	if err != nil || response.GetRetInfo().GetCode() == 0 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if len(owner.validated) != 1 || len(owner.released) != 0 {
		t.Fatalf("validated=%v released=%v", owner.validated, owner.released)
	}
}

func TestUpdateRunnerReleasesPreviousLogicalAccountBeforeChangingIt(t *testing.T) {
	service, repo, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{releaseErr: context.DeadlineExceeded}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-old")

	rejected, err := service.UpdateRunner(
		context.Background(),
		runnerUpdateRequest("logical-new"),
	)
	if err != nil || rejected.GetRetInfo().GetCode() == 0 {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
	current, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.LogicalAccountID == nil || *current.LogicalAccountID != "logical-old" {
		t.Fatalf("runner changed despite release failure: %+v", current)
	}

	owner.releaseErr = nil
	updated, err := service.UpdateRunner(
		context.Background(),
		runnerUpdateRequest("logical-new"),
	)
	if err != nil || updated.GetRetInfo().GetCode() != 0 ||
		updated.GetRunner().GetLogicalAccountId() != "logical-new" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if len(owner.released) != 2 ||
		owner.released[1] != "crypto/logical-old/runner-1" {
		t.Fatalf("released=%v", owner.released)
	}
}

func runnerUpdateRequest(logicalAccountID string) *strategypb.UpdateRunnerReq {
	return &strategypb.UpdateRunnerReq{Runner: &strategypb.StrategyRunner{
		RunnerId: "runner-1", StrategyId: "strategy-1", SpaceId: "crypto",
		ViewId: "view-2", Frequency: "5m", ParamsJson: "{}",
		LogicalAccountId: logicalAccountID,
	}}
}

type blockingLogicalAccountOwner struct {
	releaseStarted chan struct{}
	allowRelease   chan struct{}
	claimed        chan string
}

func (o *blockingLogicalAccountOwner) Validate(
	context.Context,
	string,
	string,
) error {
	return nil
}

func (o *blockingLogicalAccountOwner) Claim(
	_ context.Context,
	_ string,
	logicalAccountID string,
	_ string,
) error {
	o.claimed <- logicalAccountID
	return nil
}

func (o *blockingLogicalAccountOwner) Release(
	context.Context,
	string,
	string,
	string,
) error {
	close(o.releaseStarted)
	<-o.allowRelease
	return nil
}

func TestRunnerOwnershipChangesAreSerializedPerRunner(t *testing.T) {
	service, _, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &blockingLogicalAccountOwner{
		releaseStarted: make(chan struct{}),
		allowRelease:   make(chan struct{}),
		claimed:        make(chan string, 1),
	}
	service.LogicalAccounts = owner
	createRPCRunner(t, service, "runner-1", "strategy-1", "crypto", "logical-old")

	updateDone := make(chan *strategypb.UpdateRunnerRsp, 1)
	go func() {
		response, _ := service.UpdateRunner(
			context.Background(),
			runnerUpdateRequest("logical-new"),
		)
		updateDone <- response
	}()
	<-owner.releaseStarted

	enableDone := make(chan *strategypb.SetRunnerStatusRsp, 1)
	go func() {
		response, _ := service.SetRunnerStatus(
			context.Background(),
			&strategypb.SetRunnerStatusReq{
				RunnerId: "runner-1",
				Status:   string(domain.RunnerStatusEnabled),
			},
		)
		enableDone <- response
	}()

	select {
	case claimed := <-owner.claimed:
		close(owner.allowRelease)
		t.Fatalf("enable raced with update and claimed %q", claimed)
	case <-time.After(50 * time.Millisecond):
	}
	close(owner.allowRelease)
	if response := <-updateDone; response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("update=%+v", response)
	}
	if response := <-enableDone; response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("enable=%+v", response)
	}
	if claimed := <-owner.claimed; claimed != "logical-new" {
		t.Fatalf("claimed=%q, want logical-new", claimed)
	}
}
