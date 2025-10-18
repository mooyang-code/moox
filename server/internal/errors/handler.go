package errors

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// HandleError 统一错误处理（用于Gin）
func HandleError(c *gin.Context, err error) {
	if err == nil {
		return
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		// 记录错误日志
		if appErr.HTTPStatus >= 500 {
			log.ErrorContextf(c.Request.Context(), "Server error: %v", appErr)
		} else {
			log.WarnContextf(c.Request.Context(), "Client error: %v", appErr)
		}

		// 返回错误响应
		response := gin.H{
			"code":    appErr.Code,
			"message": appErr.Message,
		}
		if appErr.Details != nil && len(appErr.Details) > 0 {
			response["details"] = appErr.Details
		}
		c.JSON(appErr.HTTPStatus, response)
		return
	}

	// 未知错误
	log.ErrorContextf(c.Request.Context(), "Unknown error: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"code":    CodeInternal,
		"message": "Internal server error",
	})
}

// HandleErrorWithData 带数据的错误处理
func HandleErrorWithData(c *gin.Context, err error, data interface{}) {
	if err == nil {
		c.JSON(http.StatusOK, gin.H{
			"code":    CodeSuccess,
			"message": "success",
			"data":    data,
		})
		return
	}
	HandleError(c, err)
}
