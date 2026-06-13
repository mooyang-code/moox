package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/modules/storage/internal/services/access"
	accesscfg "github.com/mooyang-code/moox/modules/storage/internal/services/access/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter"
	adaptercfg "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	adapterlogic "github.com/mooyang-code/moox/modules/storage/internal/services/adapter/logic"
	"github.com/mooyang-code/moox/modules/storage/internal/services/dbmanager"
	dbmanagercfg "github.com/mooyang-code/moox/modules/storage/internal/services/dbmanager/config"
	msgsvr "github.com/mooyang-code/moox/modules/storage/internal/services/messager/server"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata"
	storagev2 "github.com/mooyang-code/moox/modules/storage/internal/services/v2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	pbv2 "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	// 清除unix域套接字文件，避免内部使用unix域套接字的服务启动失败
	clearSocketFiles()

	// 从配置文件加载所有配置
	dbmgrCfg, err := dbmanagercfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig dbmgrCfg err[%v]", err)
	}
	accCfg, err := accesscfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig accCfg err[%v]", err)
	}
	adpCfg, err := adaptercfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig adpCfg err[%v]", err)
	}

	// 创建并启动消息服务器
	_, err = msgsvr.SetupMessageServer(accCfg.MsgSvrConf)
	if err != nil {
		log.Errorf("设置消息服务器失败: %v", err)
		return
	}

	// 创建trpc服务器
	s := trpc.NewServer()

	// 数据库表管理服务
	dbmgrImp, e := dbmanager.NewDBTableManagerService(dbmgrCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterDBTableManagerService(s, dbmgrImp)

	// 元数据服务
	metaImp, e := metadata.NewMetaServicer(s)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterMetaAdminService(s, metaImp)
	pb.RegisterMetaFieldService(s, metaImp)

	// 接入层服务
	accessorImp, e := access.NewAccessor(accCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterAccessService(s, accessorImp)

	// 适配层服务
	adapterImp, e := adapter.NewAdapter(adpCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterAdapterService(s, adapterImp)
	access.SetLocalAdapterService(adapterImp)

	// 新量化金融数据协议服务。当前实现提供真实的文件型读写路径，用于承接
	// Workspace/Instrument/DataSet/DataView 等新概念和 CSV 验收数据。
	v2Service := storagev2.NewService(os.Getenv("MOOX_STORAGE_HOME"))
	pbv2.RegisterMetadataServiceService(s, v2Service)
	pbv2.RegisterDataServiceService(s, v2Service)
	pbv2.RegisterQueryServiceService(s, v2Service)
	pbv2.RegisterAdapterService(s, v2Service)

	timer.RegisterScheduler("columnOperateSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.adapter.tableOperate.timer"), dao.UpdateColumnMappings)

	// 注册数据库表定时任务
	timer.RegisterScheduler("tableOperateSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.tableOperate.timer"), adapterlogic.HandleTableSchedule)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}

func clearSocketFiles() {
	files, err := filepath.Glob("./*")
	if err != nil {
		log.Errorf("读取目录失败: %v", err)
		return
	}

	for _, file := range files {
		baseFile := filepath.Base(file)
		if strings.HasPrefix(baseFile, "0.0.0.0") || strings.HasPrefix(baseFile, "127.0.0.1") {
			if err := os.Remove(file); err != nil {
				log.Errorf("删除文件 %s 失败: %v", file, err)
			}
		}
	}
}
