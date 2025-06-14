package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/server/internal/gateway"
	_ "github.com/mooyang-code/moox/server/internal/middleware"
	authsvr "github.com/mooyang-code/moox/server/internal/service/auth"
	authcfg "github.com/mooyang-code/moox/server/internal/service/auth/config"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	// 从配置文件加载所有配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig err[%v]", err)
	}

	// 创建trpc服务器
	s := trpc.NewServer()

	// 初始化认证服务
	authImp, e := authsvr.NewAuthService(authCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterAuthAPIService(s, authImp)

	// 初始化网关服务（包括服务处理器和HTTP路由）
	log.Info("正在初始化网关服务...")
	gateway.InitGatewayServices(s)
	log.Info("网关服务初始化完成")

	// 启动trpc服务器
	log.Info("启动trpc服务器...")
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}
