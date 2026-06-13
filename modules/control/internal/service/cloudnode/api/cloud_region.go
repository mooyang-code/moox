package api

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/modules/control/internal/common"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/config"
)

// RegionInfo 地区信息
type RegionInfo struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`       // 标签（国内/海外）
	MaxNodes int    `json:"max_nodes"` // 地区最大节点数
	MaxNamespacesPerRegion   int `json:"max_namespaces_per_region"`
	MaxFunctionsPerNamespace int `json:"max_functions_per_namespace"`
}

// CloudRegionHandler 云地区处理器
type CloudRegionHandler struct {
	// 地区数据可以从配置文件或常量中读取
}

// NewCloudRegionHandler 创建云地区处理器
func NewCloudRegionHandler() *CloudRegionHandler {
	return &CloudRegionHandler{}
}

// GetRegionList 获取某个云厂商的地区列表
// GET /api/v1/cloud_region/list?provider=tencent
func (h *CloudRegionHandler) GetRegionList(c *gin.Context) {
	provider := c.Query("provider")

	// 根据云厂商获取地区列表
	regions := getRegionsByProvider(provider)
	total := int64(len(regions))

	// 使用 PaginatedListResponse 返回，格式与 ListCloudAccounts 一致
	common.PaginatedListResponse(c, "查询成功", regions, total)
}

// getRegionsByProvider 根据云厂商获取地区列表
func getRegionsByProvider(provider string) []RegionInfo {
	switch provider {
	case "tencent":
		return getTencentRegions()
	// 未来可以添加其他云厂商
	// case "aliyun":
	//     return getAliyunRegions()
	// case "aws":
	//     return getAWSRegions()
	default:
		return []RegionInfo{}
	}
}

// getTencentRegions 获取腾讯云地区列表（从配置文件读取）
func getTencentRegions() []RegionInfo {
	cfg := config.Get()
	var regions []RegionInfo
	for _, r := range cfg.CloudRegions.Tencent {
		regions = append(regions, RegionInfo{
			Code:     r.Code,
			Name:     r.Name,
			Tag:      r.Tag,
			MaxNodes: r.MaxNodes,
			MaxNamespacesPerRegion:   r.MaxNamespacesPerRegion,
			MaxFunctionsPerNamespace: r.MaxFunctionsPerNamespace,
		})
	}
	return regions
}
