package main

import (
	"context"

	"github.com/mooyang-code/moox/modules/collector/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/collector/internal/cloudfunction"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go/log"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

func main() {
	// 创建默认启动器配置
	cfg := config.DefaultConfig()

	// 创建启动器
	bs := bootstrap.New(cfg)

	// 初始化启动器（统一初始化流程：配置加载 → 服务启动 → 服务注册 → 定时器注册）
	if err := bs.Initialize(context.Background()); err != nil {
		panic("failed to initialize bootstrap: " + err.Error())
	}

	// 注册并启动云函数(云函数在这里，只是起到心跳保持的作用)，
	cloudfunction.RegisterCloudFunction()

	// 保持运行
	log.Info("数据采集器 启动完成")
	select {}
}
