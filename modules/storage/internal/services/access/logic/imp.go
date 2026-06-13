package logic

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/access/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/publisher"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

// accessorImpl 实现 Accessor 接口
type accessorImpl struct {
	cfg           *config.Config
	adapterClient pb.AdapterClientProxy
	publisher     publisher.Publisher
}

// InitAccessorImpl 服务初始化入口
func InitAccessorImpl(accessCfg *config.Config) (*accessorImpl, error) {
	var imp accessorImpl
	// 初始化缓存组件（全局统一缓存）
	if err := initCaches(accessCfg); err != nil {
		log.Fatalf("initCaches err[%v]", err)
	}

	// 初始化消息发布器（非消息服务器器）
	pub, err := initPublisher(accessCfg)
	if err != nil {
		log.Errorf("创建消息发布器失败: %v", err)
		// 这里不返回错误，允许服务在没有消息发布器的情况下运行
	}

	imp.cfg = accessCfg
	imp.adapterClient = pb.NewAdapterClientProxy(client.WithServiceName("trpc.storage.adapter.Adapter"))
	imp.publisher = pub
	return &imp, nil
}

// 初始化消息发布器
func initPublisher(cfg *config.Config) (publisher.Publisher, error) {
	if !cfg.MsgSvrConf.Enable { // 消息通知模块关闭
		return nil, nil
	}
	pub, err := publisher.NewPublisher(constants.PublisherType(cfg.MsgSvrConf.Name),
		publisher.PublisherOptions{
			ServerURL: fmt.Sprintf("%s://%s:%d",
				cfg.MsgSvrConf.Name,
				cfg.MsgSvrConf.Host,
				cfg.MsgSvrConf.Port),
			ConnectTimeout: 10 * time.Second, // 默认连接超时时间
			StreamName:     "storage_events",
			StreamSubjects: []string{
				cfg.MsgSvrConf.DataDetailModifySubject,
				cfg.MsgSvrConf.ObjectModifySubject,
			},
		})
	if err != nil {
		return nil, err
	}

	// 连接到消息服务器
	if err := pub.Connect(nil); err != nil {
		log.Errorf("连接到消息服务器失败: %v", err)
	}
	return pub, nil
}

// 初始化所有缓存
func initCaches(cfg *config.Config) error {
	// 初始化缓存组件
	if err := cache.InitSingleDBCacheWithPollingInterval(
		time.Duration(cfg.SchemaCachePollingIntervalSeconds)*time.Second,
		// 字段信息表
		cache.Field{
			AccessUrl: cfg.SchemaCaches[cache.Field{}.SchemaID()],
		},
		// 数据集信息表
		cache.Dataset{
			AccessUrl: cfg.SchemaCaches[cache.Dataset{}.SchemaID()],
		},
		// 数据对象路由表
		cache.ObjectRoute{
			AccessUrl: cfg.SchemaCaches[cache.ObjectRoute{}.SchemaID()],
		},
		// 存储实体表
		cache.StorageEntity{
			AccessUrl: cfg.SchemaCaches[cache.StorageEntity{}.SchemaID()],
		},
	); err != nil {
		log.Fatalf("InitSingleDBCache err[%v]", err)
	}
	return nil
}

// shouldSendNotification 检查指定appID和项目是否应该发送变更通知
func (i *accessorImpl) shouldSendNotification(appID string, projectID int32) bool {
	// 如果消息发布器未初始化，不发送通知
	if i.publisher == nil {
		return false
	}

	// 获取通知配置
	notificationSettings := i.cfg.NotificationSettings

	// 默认不发送，除非项目ID显式启用
	if len(notificationSettings.EnabledProjectIDs) == 0 {
		return false
	}
	enabled := false
	for _, enabledProjectID := range notificationSettings.EnabledProjectIDs {
		if enabledProjectID == projectID {
			enabled = true
			break
		}
	}
	if !enabled {
		return false
	}
	return true // 不在排除列表中，发送通知
}
