package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	archiveMaterializeTimerService = "trpc.moox.archive.materialize.timer"
	archiveCOSSyncTimerService     = "trpc.moox.archive.cos_sync.timer"
)

type archiveCOSSyncer interface {
	Sync(context.Context) error
}

func registerArchiveTimers(s *server.Server, materializer writer.Scheduler, cosSyncer archiveCOSSyncer) error {
	if s == nil {
		return fmt.Errorf("archive timers require a tRPC server")
	}
	materializeJob, err := timerjob.New("archive_materialize", 2*time.Minute, materializer.MaterializeOnce)
	if err != nil {
		return err
	}
	if err := registerArchiveTimer(s, archiveMaterializeTimerService, materializeJob); err != nil {
		return err
	}
	cosHandler := func(context.Context) error { return nil }
	if cosSyncer != nil {
		cosHandler = cosSyncer.Sync
	}
	cosJob, err := timerjob.New("archive_cos_sync", 5*time.Minute, cosHandler)
	if err != nil {
		return err
	}
	return registerArchiveTimer(s, archiveCOSSyncTimerService, cosJob)
}

func registerArchiveTimer(s *server.Server, name string, job *timerjob.Job) error {
	service := s.Service(name)
	if service == nil {
		return fmt.Errorf("archive timer service %q is not configured", name)
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
