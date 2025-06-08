package main

import (
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {

	// 创建trpc服务器
	s := trpc.NewServer()

	//pb.RegisterAdminAPIService(s, metaImp)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}
