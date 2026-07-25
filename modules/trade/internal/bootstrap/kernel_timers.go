package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	tradeFillReconcileTimerService = "trpc.moox.trade.fill_reconcile.timer"
	tradeOrderRecoveryTimerService = "trpc.moox.trade.order_recovery.timer"
)

func registerKernelTimers(s *server.Server, tradeStore *store.Store, engine *command.Engine) error {
	if s == nil || tradeStore == nil || engine == nil {
		return fmt.Errorf("trade kernel timers require server, store, and engine")
	}
	reconcileJob, err := timerjob.New("trade_fill_reconcile", 30*time.Second, func(ctx context.Context) error {
		return reconcileOrdersOnce(ctx, tradeStore, engine, "", "", "")
	})
	if err != nil {
		return err
	}
	if err := registerKernelTimer(s, tradeFillReconcileTimerService, reconcileJob); err != nil {
		return err
	}
	recoveryJob, err := timerjob.New("trade_order_recovery", 15*time.Second, func(ctx context.Context) error {
		return recoverOrdersOnce(ctx, tradeStore, engine)
	})
	if err != nil {
		return err
	}
	return registerKernelTimer(s, tradeOrderRecoveryTimerService, recoveryJob)
}

func registerKernelTimer(s *server.Server, name string, job *timerjob.Job) error {
	service := s.Service(name)
	if service == nil {
		return fmt.Errorf("trade timer service %q is not configured", name)
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
