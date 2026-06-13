package dao

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"golang.org/x/sync/singleflight"
	"trpc.group/trpc-go/trpc-go/log"
)

// DeviceConstructor 设备构造函数类型，用于创建新的存储设备实例
type DeviceConstructor func(ctx context.Context) (Storer, error)

// deviceRegistry 存储设备注册表
var deviceRegistry struct {
	once     sync.Once
	registry map[pb.EnumDeviceType]DeviceConstructor
}

// deviceInstance 设备实例包装器，包含设备实例和最后使用时间
type deviceInstance struct {
	device   Storer
	lastUsed time.Time
	mu       sync.RWMutex // 保护设备实例的并发访问
}

// devicePool 设备实例池
type devicePool struct {
	instances map[string]*deviceInstance // key: deviceID_deviceType
	mu        sync.RWMutex
	// createGroup 防止并发创建同一个设备实例导致重复打开数据库
	createGroup singleflight.Group
	// 清理配置
	maxIdleTime time.Duration // 最大空闲时间
	cleanupTick time.Duration // 清理检查间隔
}

// 全局设备池实例
var globalDevicePool *devicePool
var poolInitOnce sync.Once

// resolveConnectInfo 解析实际的连接信息
// 对于 localhost 等特殊值，会解析为实际的文件路径
func resolveConnectInfo(ctx context.Context, deviceType pb.EnumDeviceType, connectInfo string) string {
	// 只处理 localhost 的情况
	if connectInfo != "localhost" {
		if deviceType == pb.EnumDeviceType_DUCKDB_DEVICE && connectInfo == ":memory:" {
			return connectInfo
		}
		// 对于文件路径，转换为绝对路径以确保一致性
		if absPath, err := filepath.Abs(connectInfo); err == nil {
			return absPath
		}
		return connectInfo
	}

	// 处理 localhost 的情况，根据设备类型返回对应的配置路径
	cfg := config.GetGlobalConfig()
	var defaultPath string

	switch deviceType {
	case pb.EnumDeviceType_DUCKDB_DEVICE:
		if cfg != nil && cfg.DuckDB.DataPath != "" {
			defaultPath = cfg.DuckDB.DataPath
		} else {
			defaultPath = "../database/duckdb"
		}
	case pb.EnumDeviceType_ROCKDB_DEVICE:
		if cfg != nil && cfg.RocksDB.DataPath != "" {
			defaultPath = cfg.RocksDB.DataPath
		} else {
			defaultPath = "../database/rocksdb"
		}
	case pb.EnumDeviceType_BLEVE_DEVICE:
		if cfg != nil && cfg.Bleve.IndexPath != "" {
			defaultPath = cfg.Bleve.IndexPath
		} else {
			defaultPath = "../database/bleve"
		}
	case pb.EnumDeviceType_CSV_DEVICE:
		if cfg != nil && cfg.CSV.DataPath != "" {
			defaultPath = cfg.CSV.DataPath
		} else {
			defaultPath = "../database/csv"
		}
	default:
		return connectInfo
	}

	// 转换为绝对路径
	if absPath, err := filepath.Abs(defaultPath); err == nil {
		log.DebugContextf(ctx, "解析连接信息: %s -> %s (设备类型: %s)", connectInfo, absPath, deviceType)
		return absPath
	}
	return defaultPath
}

// 初始化设备注册表
func initDeviceRegistry() {
	deviceRegistry.once.Do(func() {
		deviceRegistry.registry = make(map[pb.EnumDeviceType]DeviceConstructor)
	})
}

// initDevicePool 初始化设备池
func initDevicePool() {
	poolInitOnce.Do(func() {
		globalDevicePool = &devicePool{
			instances:   make(map[string]*deviceInstance),
			maxIdleTime: 30 * time.Minute, // 30分钟空闲时间
			cleanupTick: 5 * time.Minute,  // 5分钟清理一次
		}
		// 启动清理协程
		go globalDevicePool.startCleanup()
	})
}

// startCleanup 启动定期清理过期设备实例的协程
func (dp *devicePool) startCleanup() {
	ticker := time.NewTicker(dp.cleanupTick)
	defer ticker.Stop()

	for range ticker.C {
		dp.cleanup()
	}
}

// cleanup 清理过期的设备实例
func (dp *devicePool) cleanup() {
	now := time.Now()
	var expired []*deviceInstance

	dp.mu.Lock()
	for key, instance := range dp.instances {
		instance.mu.RLock()
		isExpired := now.Sub(instance.lastUsed) > dp.maxIdleTime
		instance.mu.RUnlock()

		if isExpired {
			log.Debugf("清理过期设备实例: %s", key)
			delete(dp.instances, key)
			expired = append(expired, instance)
		}
	}
	dp.mu.Unlock()

	for _, instance := range expired {
		instance.mu.Lock()
		device := instance.device
		instance.device = nil
		instance.mu.Unlock()
		if device == nil {
			continue
		}
		if err := device.CloseDeviceConn(); err != nil {
			log.Warnf("清理设备实例连接失败: %v", err)
		}
	}
}

// RegisterDeviceType 注册设备类型到系统
// 在dao下新增存储设备，自注册之后，注意需要在logic/imp.go中import，触发包初始化
// deviceType: 设备类型
// constructor: 设备构造函数
func RegisterDeviceType(deviceType pb.EnumDeviceType, constructor DeviceConstructor) {
	initDeviceRegistry()
	deviceRegistry.registry[deviceType] = constructor
}

// getDeviceFromPool 从设备池获取设备实例
func (dp *devicePool) getDeviceFromPool(ctx context.Context, deviceID int,
	deviceInfo *cache.StorageDevice) (Storer, error) {
	deviceType := pb.EnumDeviceType(deviceInfo.DeviceType)

	// 解析实际连接路径（处理 localhost 等特殊情况）
	actualConnInfo := resolveConnectInfo(ctx, deviceType, deviceInfo.ConnInfo)

	// 使用 deviceType + 实际连接路径 作为池的 key，避免相同连接信息被重复打开
	// 这对于 RocksDB 等不支持同时打开同一数据库的存储引擎至关重要
	poolKey := fmt.Sprintf("%d_%s", deviceType, actualConnInfo)

	// 先尝试从池中获取
	dp.mu.RLock()
	if instance, exists := dp.instances[poolKey]; exists {
		instance.mu.Lock()
		instance.lastUsed = time.Now()
		device := instance.device
		instance.mu.Unlock()
		dp.mu.RUnlock()

		log.Debugf("从设备池获取设备实例: %s (deviceID=%d)", poolKey, deviceID)
		return device, nil
	}
	dp.mu.RUnlock()

	// 池中不存在，使用 singleflight 防止并发重复创建
	res, err, _ := dp.createGroup.Do(poolKey, func() (any, error) {
		// 二次检查，避免其它协程已经创建成功
		dp.mu.RLock()
		if instance, exists := dp.instances[poolKey]; exists {
			instance.mu.Lock()
			instance.lastUsed = time.Now()
			device := instance.device
			instance.mu.Unlock()
			dp.mu.RUnlock()
			log.Debugf("从设备池获取设备实例: %s (deviceID=%d)", poolKey, deviceID)
			return device, nil
		}
		dp.mu.RUnlock()

		return dp.createAndCacheDevice(ctx, poolKey, deviceID, deviceInfo, actualConnInfo)
	})
	if err != nil {
		return nil, err
	}
	device, ok := res.(Storer)
	if !ok {
		return nil, fmt.Errorf("设备实例类型不匹配: %T", res)
	}
	return device, nil
}

// createAndCacheDevice 创建新设备实例并缓存到池中
func (dp *devicePool) createAndCacheDevice(ctx context.Context, poolKey string, deviceID int,
	deviceInfo *cache.StorageDevice, actualConnInfo string) (Storer, error) {
	deviceType := pb.EnumDeviceType(deviceInfo.DeviceType)

	// 获取构造函数
	constructor, exists := deviceRegistry.registry[deviceType]
	if !exists {
		return nil, fmt.Errorf("无效的设备类型: %d", deviceType)
	}

	// 创建设备实例
	device, err := constructor(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建设备实例失败: %v", err)
	}

	// 初始化设备连接
	log.DebugContextf(ctx, "为设备[%d]创建连接，poolKey=%s, connInfo=%s", deviceID, poolKey, actualConnInfo)
	err = device.GetDeviceConn(actualConnInfo)
	if err != nil {
		log.ErrorContextf(ctx, "连接设备[%d]失败: %v", deviceID, err)
		return nil, fmt.Errorf("连接设备失败: %v", err)
	}

	// 缓存到池中
	dp.mu.Lock()
	// 双重检查，避免并发创建
	if instance, exists := dp.instances[poolKey]; exists {
		dp.mu.Unlock()
		// 如果已经有实例了，关闭刚创建的实例以释放资源
		if closeErr := device.CloseDeviceConn(); closeErr != nil {
			log.WarnContextf(ctx, "关闭重复创建的设备实例失败: %v", closeErr)
		}
		instance.mu.Lock()
		instance.lastUsed = time.Now()
		cachedDevice := instance.device
		instance.mu.Unlock()
		log.DebugContextf(ctx, "使用并发创建的设备实例: %s (deviceID=%d)", poolKey, deviceID)
		return cachedDevice, nil
	}

	dp.instances[poolKey] = &deviceInstance{
		device:   device,
		lastUsed: time.Now(),
	}
	dp.mu.Unlock()

	log.InfoContextf(ctx, "创建并缓存新设备实例: %s (deviceID=%d, connInfo=%s)", poolKey, deviceID, deviceInfo.ConnInfo)
	return device, nil
}

// InternalDuckDBDeviceID 系统内部使用的设备ID常量
const InternalDuckDBDeviceID = 10000

// NewStoreDevice 存储设备工厂函数（优化版本，使用设备池）
// 注意：该函数根据设备类型创建对应的存储设备对象，并使用设备池进行缓存复用
func NewStoreDevice(ctx context.Context, deviceID int) (Storer, error) {
	initDeviceRegistry()
	initDevicePool()

	// 初始化设备信息
	var deviceInfo *cache.StorageDevice
	if deviceID == InternalDuckDBDeviceID { // DuckDB-内存版，系统内部使用
		deviceInfo = &cache.StorageDevice{
			DeviceID:   InternalDuckDBDeviceID,
			DeviceName: "DuckDB-内存版",
			DeviceType: int(pb.EnumDeviceType_DUCKDB_DEVICE),
			ConnInfo:   ":memory:",
		}
	} else {
		deviceInfo = cache.GetStorageDeviceInfo(deviceID)
	}
	if deviceInfo == nil {
		return nil, fmt.Errorf("设备信息不存在: %d", deviceID)
	}

	// 从设备池获取或创建设备实例
	device, err := globalDevicePool.getDeviceFromPool(ctx, deviceID, deviceInfo)
	if err != nil {
		return nil, err
	}

	log.DebugContextf(ctx, "获取设备实例: %s", device.GetDeviceKey())
	return device, nil
}
