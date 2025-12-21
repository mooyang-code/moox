package api

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
)

// RegionInfo 地区信息
type RegionInfo struct {
	Code string `json:"code"`
	Name string `json:"name"`
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

// getTencentRegions 获取腾讯云地区列表
func getTencentRegions() []RegionInfo {
	return []RegionInfo{
		{Code: "ap-bangkok", Name: "亚太东南（曼谷）"},
		{Code: "ap-beijing", Name: "华北地区（北京）"},
		{Code: "ap-chengdu", Name: "西南地区（成都）"},
		{Code: "ap-chongqing", Name: "西南地区（重庆）"},
		{Code: "ap-guangzhou", Name: "华南地区（广州）"},
		{Code: "ap-hongkong", Name: "港澳台地区（中国香港）"},
		{Code: "ap-jakarta", Name: "亚太东南（雅加达）"},
		{Code: "ap-nanjing", Name: "华东地区（南京）"},
		{Code: "ap-seoul", Name: "亚太东北（首尔）"},
		{Code: "ap-shanghai", Name: "华东地区（上海）"},
		{Code: "ap-shanghai-fsi", Name: "华东地区（上海金融）"},
		{Code: "ap-shenzhen-fsi", Name: "华南地区（深圳金融）"},
		{Code: "ap-singapore", Name: "亚太东南（新加坡）"},
		{Code: "ap-tokyo", Name: "亚太东北（东京）"},
		{Code: "eu-frankfurt", Name: "欧洲地区（法兰克福）"},
		{Code: "na-ashburn", Name: "美国东部（弗吉尼亚）"},
		{Code: "na-siliconvalley", Name: "美国西部（硅谷）"},
		{Code: "sa-saopaulo", Name: "南美地区（圣保罗）"},
	}
}

