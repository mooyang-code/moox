package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"

	"github.com/gin-gonic/gin"
)

// FunctionPackageHandler 云函数代码包处理器
type FunctionPackageHandler struct {
	packageService *logic.FunctionPackageService
}

// NewFunctionPackageHandler 创建云函数代码包处理器
func NewFunctionPackageHandler(packageService *logic.FunctionPackageService) *FunctionPackageHandler {
	return &FunctionPackageHandler{
		packageService: packageService,
	}
}

// UploadPackageAsync 异步上传代码包
// @Summary 异步上传云函数代码包
// @Description 异步上传云函数代码包到COS并记录到数据库
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param request body logic.UploadPackageRequest true "上传请求"
// @Success 200 {object} APIResponse{data=logic.UploadPackageAsyncResponse}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/upload-async [post]
func (h *FunctionPackageHandler) UploadPackageAsync(c *gin.Context) {
	var req logic.UploadPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
		return
	}

	// 设置创建者为固定值
	req.CreatedBy = "moox"

	resp, err := h.packageService.UploadPackageAsync(c.Request.Context(), &req)
	if err != nil {
		// 根据错误类型返回不同的HTTP状态码
		if businessErr, ok := err.(*logic.BusinessError); ok {
			switch {
			case errors.Is(businessErr, logic.ErrValidationFailed):
				ErrorResponse(c, http.StatusBadRequest, "参数验证失败", err)
			case errors.Is(businessErr, logic.ErrVersionExists):
				ErrorResponse(c, http.StatusConflict, "版本冲突，请修改版本号重试", err)
			case errors.Is(businessErr, logic.ErrResourceNotFound):
				ErrorResponse(c, http.StatusNotFound, "资源未找到", err)
			default:
				ErrorResponse(c, http.StatusBadRequest, "请求失败", err)
			}
		} else {
			ErrorResponse(c, http.StatusInternalServerError, "异步上传失败", err)
		}
		return
	}
	SuccessResponse(c, "异步上传任务创建成功", resp)
}

// GetUploadTaskStatus 获取上传任务状态
// @Summary 获取上传任务状态
// @Description 获取异步上传任务的执行状态
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param task_id path string true "任务ID"
// @Success 200 {object} APIResponse{data=logic.UploadTaskStatusResponse}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/upload-task/{task_id}/status [get]
func (h *FunctionPackageHandler) GetUploadTaskStatus(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "任务ID不能为空", nil)
		return
	}

	resp, err := h.packageService.GetUploadTaskStatus(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "获取任务状态失败", err)
		return
	}

	SuccessResponse(c, "获取任务状态成功", resp)
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
// @Success 200 {object} APIResponse{data=logic.PackageListResponse}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages [get]
func (h *FunctionPackageHandler) GetPackageList(c *gin.Context) {
	var req logic.PackageListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
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

	resp, err := h.packageService.GetPackageList(c.Request.Context(), &req)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "查询失败", err)
		return
	}

	// 使用新的分页列表响应格式：total在外层，items直接作为data数组
	PaginatedListResponse(c, "查询成功", resp.Items, resp.Total)
}

// GetPackageDetail 获取代码包详情
// @Summary 获取云函数代码包详情
// @Description 根据ID获取云函数代码包详细信息，包含所有字段和显示标签，返回数组格式
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param id path int true "代码包ID"
// @Success 200 {object} APIResponse{data=[]logic.PackageDetail}
// @Failure 400 {object} APIResponse
// @Failure 404 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{id} [get]
func (h *FunctionPackageHandler) GetPackageDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "无效的ID参数", err)
		return
	}

	pkg, err := h.packageService.GetPackageDetail(c.Request.Context(), id)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "代码包不存在", err)
		return
	}

	// 按照moox统一格式，data返回数组格式
	SuccessResponse(c, "查询成功", []*logic.PackageDetail{pkg})
}

// DeletePackage 删除代码包
// @Summary 删除云函数代码包
// @Description 软删除云函数代码包
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param id path int true "代码包ID"
// @Success 200 {object} APIResponse
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{id} [delete]
func (h *FunctionPackageHandler) DeletePackage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "无效的ID参数", err)
		return
	}

	err = h.packageService.DeletePackage(c.Request.Context(), id)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "删除失败", err)
		return
	}

	SuccessResponse(c, "删除成功", nil)
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
	req := &logic.PackageListRequest{
		Page:        1,
		PageSize:    1000, // 获取所有可用的包
		PackageType: packageType,
		Status:      &[]int{1}[0], // 只获取可用状态的包
	}

	resp, err := h.packageService.GetPackageList(c.Request.Context(), req)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "查询失败", err)
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
			ID: item.ID,
			Label: fmt.Sprintf("[%s] %s %s (%s) - %s",
				item.PackageTypeLabel,
				displayName,
				item.Version,
				item.Runtime,
				item.CreatedAt.Format("2006-01-02")),
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
// @Description 获取代码包的直接下载URL，用于浏览器直接下载
// @Tags 云函数代码包
// @Accept json
// @Produce json
// @Param id path int true "代码包ID"
// @Success 200 {object} APIResponse{data=logic.PackageDownloadURL}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/function-packages/{id}/download-url [get]
func (h *FunctionPackageHandler) GetPackageDownloadURL(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "无效的ID参数", err)
		return
	}

	result, err := h.packageService.GetPackageDownloadURL(c.Request.Context(), id)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "获取下载URL失败", err)
		return
	}
	SuccessResponse(c, "获取下载URL成功", result)
}
