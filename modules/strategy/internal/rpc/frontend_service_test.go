package rpc

import (
	"context"
	"testing"

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
