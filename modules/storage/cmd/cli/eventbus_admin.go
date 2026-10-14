package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// 维护命令对 EventBus 的操作（查看消费者过滤、删除消费者、清除数据集事件）都经网关交给 Admin 代为执行：
// EventBus 的管理员令牌只存在 Admin 的数据库里，Storage 主机上不放管理员凭据文件，
// 命令行用的是 storage-view 自己的网关身份（与 storage-view 进程同一份配置、同一把签名密钥）。

const sysDeployService = "trpc.moox.ops.SysDeploy"

// eventBusAdmin 是维护命令需要的 EventBus 管理操作。
type eventBusAdmin interface {
	// Inspect 返回 durables 各自是否存在及其过滤主题；流不存在时报错。
	Inspect(ctx context.Context, stream string, durables []string) (map[string]viewConsumerFilterState, error)
	// DeleteConsumers 删除消费者，不存在的跳过；返回实际删除的消费者（出错时也返回已删除的）。
	DeleteConsumers(ctx context.Context, stream string, durables []string) ([]string, error)
	// PurgeSubjects 清除流里这些主题的消息。
	PurgeSubjects(ctx context.Context, stream string, subjects []string) error
	Close() error
}

// openEventBusAdmin 打开 EventBus 管理通道；测试里替换成替身。
var openEventBusAdmin = openGatewayEventBusAdmin

// gatewayInvoker 是经网关调用 Admin 所需的最小能力（gatewayclient.Client 满足）。
type gatewayInvoker interface {
	Invoke(ctx context.Context, service, method string, req, rsp interface{}) error
	Close() error
}

type gatewayEventBusAdmin struct {
	gateway gatewayInvoker
}

// openGatewayEventBusAdmin 按 storage-view 的网关客户端配置打开通道。
// 原生部署里网关配置在 <包根>/storage-view/config/storage.yaml，否则就是 --storage-conf 指定的那份。
func openGatewayEventBusAdmin(storageConf, packageRoot string) (eventBusAdmin, error) {
	configPath := storageConf
	if _, _, view, ok := nativeComponentDirs(packageRoot); ok {
		configPath = filepath.Join(view, "config", "storage.yaml")
	}
	if strings.TrimSpace(configPath) == "" {
		return nil, errors.New("缺少 storage-view 的配置文件，无法经网关访问 Admin；请用 --storage-conf 指定")
	}
	config, err := loadRuntimeConfig(configPath)
	if err != nil {
		return nil, err
	}
	if config.GatewayClient.Caller != "storage-view" {
		return nil, fmt.Errorf("%s 里的 gateway_client.caller 必须是 storage-view（EventBus 维护只授权给这个身份），当前是 %q", configPath, config.GatewayClient.Caller)
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	// 目录缓存放在组件自己的 var 下，与 storage-view 进程各用各的，互不干扰。
	dataDirectory := filepath.Join(filepath.Dir(filepath.Dir(absolute)), "var", "storage-cli")
	client, err := config.GatewayClient.OpenInternal(absolute, dataDirectory, nil)
	if err != nil {
		return nil, fmt.Errorf("打开网关客户端失败（配置 %s）: %w", configPath, err)
	}
	return gatewayEventBusAdmin{gateway: client}, nil
}

var _ gatewayInvoker = (*gatewayclient.Client)(nil)

func (a gatewayEventBusAdmin) Close() error { return a.gateway.Close() }

func (a gatewayEventBusAdmin) call(ctx context.Context, method string, req, rsp interface{}) error {
	if err := a.gateway.Invoke(ctx, sysDeployService, method, req, rsp); err != nil {
		return fmt.Errorf("经网关调用 Admin %s 失败: %w", method, err)
	}
	return nil
}

// retError 把 Admin 返回的失败转成错误；成功时返回 nil。
func retError(method string, ret *adminpb.RetInfo) error {
	if ret == nil || ret.GetCode() == adminpb.ErrorCode_SUCCESS {
		return nil
	}
	return fmt.Errorf("Admin %s 失败: %s", method, ret.GetMsg())
}

func (a gatewayEventBusAdmin) Inspect(ctx context.Context, stream string, durables []string) (map[string]viewConsumerFilterState, error) {
	var rsp adminpb.InspectEventBusRsp
	if err := a.call(ctx, "InspectEventBus", &adminpb.InspectEventBusReq{Stream: stream, Durables: durables}, &rsp); err != nil {
		return nil, err
	}
	if err := retError("InspectEventBus", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	result := make(map[string]viewConsumerFilterState, len(rsp.GetConsumers()))
	for _, consumer := range rsp.GetConsumers() {
		result[consumer.GetDurable()] = viewConsumerFilterState{
			Exists: consumer.GetExists(), Filters: append([]string(nil), consumer.GetFilterSubjects()...), SingleFilter: consumer.GetFilterSubject(),
		}
	}
	for _, durable := range durables {
		if _, ok := result[durable]; !ok {
			return nil, fmt.Errorf("Admin 返回的结果里没有消费者 %s", durable)
		}
	}
	return result, nil
}

func (a gatewayEventBusAdmin) DeleteConsumers(ctx context.Context, stream string, durables []string) ([]string, error) {
	var rsp adminpb.DeleteEventBusConsumersRsp
	if err := a.call(ctx, "DeleteEventBusConsumers", &adminpb.DeleteEventBusConsumersReq{Stream: stream, Durables: durables}, &rsp); err != nil {
		return nil, err
	}
	return rsp.GetDeleted(), retError("DeleteEventBusConsumers", rsp.GetRetInfo())
}

func (a gatewayEventBusAdmin) PurgeSubjects(ctx context.Context, stream string, subjects []string) error {
	var rsp adminpb.PurgeEventBusSubjectsRsp
	if err := a.call(ctx, "PurgeEventBusSubjects", &adminpb.PurgeEventBusSubjectsReq{Stream: stream, Subjects: subjects}, &rsp); err != nil {
		return err
	}
	return retError("PurgeEventBusSubjects", rsp.GetRetInfo())
}

// loadRuntimeConfig 读取 storage.yaml 的完整运行配置（含 gateway_client）。
func loadRuntimeConfig(configPath string) (storageconfig.RuntimeConfig, error) {
	var cfg storageconfig.RuntimeConfig
	if configPath == "" {
		cfg.ApplyDefaults()
		return cfg, nil
	}
	if err := storageconfig.NewConfigLoader(filepath.Dir(configPath)).LoadConfigWithDefaults(filepath.Base(configPath), &cfg, cfg.ApplyDefaults); err != nil {
		return cfg, fmt.Errorf("load storage config %s: %w", configPath, err)
	}
	return cfg, nil
}

// checkViewConsumers 在发请求前按 Admin 同样的规则检查消费者名，错误能早一步、说得更清楚。
func checkViewConsumers(durables ...string) error {
	if len(durables) == 0 {
		return errors.New("至少指定一个消费者")
	}
	for _, durable := range durables {
		if !events.IsStorageViewDurable(durable) {
			return fmt.Errorf("不支持的消费者 %q，只允许 %s 及按数据集动态创建的 <misc|factor>_<12位十六进制>", durable, strings.Join(events.StorageViewConsumerDurables, "、"))
		}
	}
	return nil
}
