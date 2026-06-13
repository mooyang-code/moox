// Package cache 提供统一的缓存管理功能，支持数据库配置表的缓存操作
package cache

import (
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/go-commlib/apicache"
	wuji "github.com/mooyang-code/go-commlib/open-wuji/wujiclient"
	wujilog "github.com/mooyang-code/go-commlib/open-wuji/wujiclient/log"
	"trpc.group/trpc-go/trpc-go/log"
)

var singleDBCache apicache.ConfigCacher

var (
	wujiFiltersOnce  sync.Once
	wujiFilters      map[string]wuji.FilterInterface
	wujiFiltersMutex sync.RWMutex
)

type wujiCache struct{}

func getWujiFilters() map[string]wuji.FilterInterface {
	wujiFiltersOnce.Do(func() {
		wujiFilters = make(map[string]wuji.FilterInterface)
	})
	return wujiFilters
}

// InitSingleDBCache 初始化缓存
func InitSingleDBCache(tbItems ...apicache.APICacher) (err error) {
	return InitSingleDBCacheWithPollingInterval(0, tbItems...)
}

// InitSingleDBCacheWithPollingInterval 初始化缓存并设置无极轮询间隔
func InitSingleDBCacheWithPollingInterval(pollingInterval time.Duration, tbItems ...apicache.APICacher) error {
	wujilog.SetLog(log.DefaultLogger)

	filters := getWujiFilters()
	wujiFiltersMutex.Lock()
	defer wujiFiltersMutex.Unlock()

	for _, tb := range tbItems {
		schemaID := tb.SchemaID()

		// 如果已存在该表的filter，则跳过(避免重复初始化)
		if _, exists := filters[schemaID]; exists {
			continue
		}

		options := []wuji.Option{
			wuji.WithSchemaID(schemaID),
			wuji.WithRequestURL(tb.URL()),
			wuji.WithRequestDirect(),
			wuji.EnableFilter(),
			wuji.EnsureStrongConsistency(),
		}
		if pollingInterval > 0 {
			options = append(options, wuji.WithPollingInterval(pollingInterval))
		}

		searchKeys := tb.SearchFields()[schemaID]
		filter, err := wuji.NewClientWithFilter(strings.Split(searchKeys, "|"), tb.FilterKey(), tb, options...)
		if err != nil {
			return err
		}
		filters[schemaID] = filter
	}

	singleDBCache = &wujiCache{}
	return nil
}

// GetSingeDBCache 获取缓存单例
var GetSingeDBCache = func() apicache.ConfigCacher {
	return singleDBCache
}

// GetDataItem 查询缓存信息
func (c *wujiCache) GetDataItem(schemaID, searchKey string) any {
	wujiFiltersMutex.RLock()
	defer wujiFiltersMutex.RUnlock()

	filters := getWujiFilters()
	filter, ok := filters[schemaID]
	if !ok {
		return nil
	}
	return filter.Get(searchKey)
}

// GetAll 获取全部缓存
func (c *wujiCache) GetAll(schemaID string) any {
	wujiFiltersMutex.RLock()
	defer wujiFiltersMutex.RUnlock()

	filters := getWujiFilters()
	filter, ok := filters[schemaID]
	if !ok {
		return nil
	}
	return filter.GetALL()
}

// GetKeys 获取全部缓存数据key
func (c *wujiCache) GetKeys(schemaID string) []string {
	wujiFiltersMutex.RLock()
	defer wujiFiltersMutex.RUnlock()

	filters := getWujiFilters()
	filter, ok := filters[schemaID]
	if !ok {
		return nil
	}
	return filter.GetKeys()
}

// QueryDataItem 查询缓存信息
func QueryDataItem(schemaID, searchKey string) any {
	c := GetSingeDBCache()
	if c == nil {
		return nil
	}
	return c.GetDataItem(schemaID, searchKey)
}

// GetAll 获取全部缓存
func GetAll(schemaID string) any {
	c := GetSingeDBCache()
	if c == nil {
		return nil
	}
	return c.GetAll(schemaID)
}

// GetKeys 获取全部缓存数据key
func GetKeys(schemaID string) []string {
	c := GetSingeDBCache()
	if c == nil {
		return nil
	}
	return c.GetKeys(schemaID)
}
