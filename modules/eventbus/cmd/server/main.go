package main

import (
	"github.com/mooyang-code/moox/modules/eventbus/internal/bootstrap"
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
	trpclog.InstallServiceName("eventbus")
	server, err := bootstrap.Initialize(ctx, s)
	if err != nil {
		// Fatalf 写完会先把日志队列发完再退出。EventBus 自己起不来时这一条发不到 Monitor，只在本机文件里。
		log.Fatalf("eventbus bootstrap failed: %v", err)
	}
	if err := server.Serve(); err != nil {
		// Fatalf 写完会先把日志队列发完再退出。EventBus 自己起不来时这一条发不到 Monitor，只在本机文件里。
		log.Fatalf("eventbus server failed: %v", err)
	}
}
