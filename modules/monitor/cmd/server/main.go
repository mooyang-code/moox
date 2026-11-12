package main

import (
	"github.com/mooyang-code/moox/modules/monitor/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	ctx := trpc.BackgroundContext()
	s := trpc.NewServer()
	trpclog.InstallServiceName("monitor")
	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		// Fatalf 写完会先把日志发出去再退出；Errorf 之后直接退出的话，这最后一条来不及送到 Monitor。
		log.Fatalf("monitor bootstrap failed: %v", err)
	}
	if err := server.Serve(); err != nil {
		// Fatalf 写完会先把日志发出去再退出；Errorf 之后直接退出的话，这最后一条来不及送到 Monitor。
		log.Fatalf("monitor server failed: %v", err)
	}
}
