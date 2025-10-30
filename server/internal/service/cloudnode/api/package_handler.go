package api

import (
	"fmt"

	"github.com/mooyang-code/moox/server/internal/errors"
	cloudnodemgr "github.com/mooyang-code/moox/server/internal/service/cloudnode"

	"github.com/gin-gonic/gin"
)

// FunctionPackageHandler 云函数代码包处理器
type FunctionPackageHandler struct {
	service cloudnodemgr.Service
}

// NewFunctionPackageHandler 创建云函数代码包处理器
func NewFunctionPackageHandler(service cloudnodemgr.Service) *FunctionPackageHandler {
	return &FunctionPackageHandler{
		service: service,
	}
}

// GetPackageList 获取代码包列表
// @Summary 获取云函数代码包列表
// @Description 分页查询云函数代码包列表
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量" default(20)
// @Param package_name query string false "代码包名称"
// @Param runtime query string false "运行时环境"
// @Param package_type query string false "函数包类型"
// @Param status query int false "状态"
// @Success 200 {object} APIResponse{data=PackageListResponse}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages [get]
func (h *FunctionPackageHandler) GetPackageList(c *gin.Context) {
	var req cloudnodemgr.PackageListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// 设置默认值
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}
	if req.PageSize > 100 {
		req.PageSize = 100 // 限制最大页面大小
	}

	resp, err := h.service.GetPackageList(c.Request.Context(), &req)
	if err != nil {
		HandleAppError(c, errors.Internal("查询失败", err))
		return
	}

	// 使用新的分页列表响应格式：total在外层，items直接作为data数组
	PaginatedListResponse(c, "查询成功", resp.Items, resp.Total)
}

// GetPackageDetail 获取代码包详情
// @Summary 获取云函数代码包详情
// @Description 根据package_id获取云函数代码包详细信息，包含所有字段和显示标签，返回数组格式
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param package_id path string true "代码包ID(11位随机字符串)"
// @Success 200 {object} APIResponse{data=[]PackageDetail}
// @Failure 400 {object} APIResponse
// @Failure 404 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{package_id} [get]
func (h *FunctionPackageHandler) GetPackageDetail(c *gin.Context) {
	packageID := c.Param("package_id")
	if packageID == "" {
		HandleAppError(c, errors.InvalidParam("package_id", "package_id不能为空"))
		return
	}

	// 使用GetPackageDetail方法直接获取详情（已包含转换）
	pkg, err := h.service.GetPackageDetail(c.Request.Context(), packageID)
	if err != nil {
		HandleAppError(c, errors.NotFound("代码包"))
		return
	}

	// 按照moox统一格式，data返回数组格式
	SuccessResponse(c, "查询成功", []*cloudnodemgr.PackageDetail{pkg})
}

// DeletePackage 删除代码包
// @Summary 删除云函数代码包
// @Description 软删除云函数代码包（根据package_id）
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param package_id path string true "代码包ID(11位随机字符串)"
// @Success 200 {object} APIResponse
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{package_id} [delete]
func (h *FunctionPackageHandler) DeletePackage(c *gin.Context) {
	packageID := c.Param("package_id")
	if packageID == "" {
		HandleAppError(c, errors.InvalidParam("package_id", "package_id不能为空"))
		return
	}

	err := h.service.DeletePackage(c.Request.Context(), packageID)
	if err != nil {
		HandleAppError(c, errors.Internal("删除失败", err))
		return
	}

	SuccessResponse(c, "删除成功", []interface{}{})
}

// GetPackageOptions 获取代码包选项（用于下拉选择）
// @Summary 获取代码包选项
// @Description 获取可用的代码包选项，用于批量部署时的下拉选择
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param package_type query string false "函数包类型"
// @Success 200 {object} APIResponse{data=[]PackageOptionVO}
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/options [get]
func (h *FunctionPackageHandler) GetPackageOptions(c *gin.Context) {
	packageType := c.Query("package_type")

	// 构建查询条件
	req := &cloudnodemgr.PackageListRequest{
		Page:        1,
		PageSize:    1000, // 获取所有可用的包
		PackageType: packageType,
		Status:      &[]int{1}[0], // 只获取可用状态的包
	}

	resp, err := h.service.GetPackageList(c.Request.Context(), req)
	if err != nil {
		HandleAppError(c, errors.Internal("查询失败", err))
		return
	}

	// 转换为选项格式
	options := make([]PackageOptionVO, len(resp.Items))
	for i, item := range resp.Items {
		displayName := item.PackageName
		if item.PackageType == "data_collector" {
			displayName = "数据采集器"
		}

		options[i] = PackageOptionVO{
			PackageID: item.PackageID,
			Label: fmt.Sprintf("[%s] %s %s (%s) - %s",
				item.PackageTypeLabel,
				displayName,
				item.Version,
				item.Runtime,
				item.CreateTime.Format("2006-01-02")),
			PackageName: item.PackageName,
			Version:     item.Version,
			Runtime:     item.Runtime,
			PackageType: item.PackageType,
		}
	}

	SuccessResponse(c, "查询成功", options)
}

// GetPackageDownloadURL 获取代码包下载URL
// @Summary 获取云函数代码包下载URL
// @Description 获取代码包的直接下载URL，用于浏览器直接下载（根据package_id）
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param package_id path string true "代码包ID(11位随机字符串)"
// @Success 200 {object} APIResponse{data=PackageDownloadURL}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{package_id}/download-url [get]
func (h *FunctionPackageHandler) GetPackageDownloadURL(c *gin.Context) {
	packageID := c.Param("package_id")
	if packageID == "" {
		HandleAppError(c, errors.InvalidParam("package_id", "package_id不能为空"))
		return
	}

	result, err := h.service.GetPackageDownloadURL(c.Request.Context(), packageID)
	if err != nil {
		HandleAppError(c, errors.Internal("获取下载URL失败", err))
		return
	}
	SuccessResponse(c, "获取下载URL成功", []interface{}{result})
}

// UploadPackage 上传代码包
// @Summary 上传云函数代码包
// @Description 上传云函数代码包到COS存储
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param request body UploadPackageRequest true "上传请求"
// @Success 200 {object} APIResponse{data=UploadPackageResponse}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/upload [post]
func (h *FunctionPackageHandler) UploadPackage(c *gin.Context) {
	var req cloudnodemgr.UploadPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// 从JWT中获取用户信息（不再使用）
	// if userID, exists := c.Get("user_id"); exists {
	//     req.CreatedBy = fmt.Sprintf("%v", userID)
	// }

	resp, err := h.service.UploadPackage(c.Request.Context(), &req)
	if err != nil {
		HandleAppError(c, errors.Internal("上传失败", err))
		return
	}

	SuccessResponse(c, "上传成功", []interface{}{resp})
}
