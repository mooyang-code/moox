package main

import (
	"context"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	runtimeboot "github.com/mooyang-code/moox/modules/collector/internal/app/runtimeboot"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

var Version string

func main() {
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
		runtimeapp.UpdateNodeInfo("", Version)
	}
	bs := runtimeboot.New(cfg)
	if err := bs.Initialize(context.Background()); err != nil {
		panic("failed to initialize bootstrap: " + err.Error())
	}

	serverless.RegisterCloudFunction()

	log.Info("数据采集器 SCF runtime 启动完成")
	select {}
}
