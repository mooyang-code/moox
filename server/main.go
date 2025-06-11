package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
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

	authImp, e := authsvr.NewAuthService(authCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterAuthAPIService(s, authImp)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}
