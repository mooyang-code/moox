package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// WebSocket升级器
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

// ContainerInfo 容器信息结构体
type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	Network string `json:"network"`
	Created string `json:"created"`
	Ports   string `json:"ports"`
	Command string `json:"command"`
}

// DockerStatsInfo Docker stats信息
type DockerStatsInfo struct {
	Container string `json:"Container"`
	Name      string `json:"Name"`
	ID        string `json:"ID"`
	CPUPerc   string `json:"CPUPerc"`
	MemUsage  string `json:"MemUsage"`
	MemPerc   string `json:"MemPerc"`
	NetIO     string `json:"NetIO"`
	BlockIO   string `json:"BlockIO"`
	PIDs      string `json:"PIDs"`
}

// GetContainerList 获取容器列表
func GetContainerList(c *gin.Context) {
	// 执行docker ps命令获取容器列表
	cmd := exec.Command("docker", "ps", "-a", "--format", "table {{.ID}}\\t{{.Names}}\\t{{.Image}}\\t{{.Status}}\\t{{.Ports}}\\t{{.Command}}\\t{{.CreatedAt}}")
	output, err := cmd.Output()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "获取容器列表失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	// 解析docker ps输出
	containers := parseDockerPsOutput(string(output))

	// 获取容器资源使用情况
	for i := range containers {
		stats := getContainerStats(containers[i].ID)
		if stats != nil {
			containers[i].CPU = stats.CPUPerc
			containers[i].Memory = stats.MemUsage
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "success",
		"data": containers,
	})
}

// parseDockerPsOutput 解析docker ps命令输出
func parseDockerPsOutput(output string) []ContainerInfo {
	lines := strings.Split(output, "\n")
	var containers []ContainerInfo

	// 跳过标题行
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 6 {
			container := ContainerInfo{
				ID:      fields[0],
				Name:    fields[1],
				Image:   fields[2],
				Status:  parseStatus(strings.Join(fields[3:], " ")),
				Created: parseCreatedTime(fields),
			}

			// 解析端口信息
			if len(fields) > 4 {
				container.Ports = fields[4]
			}

			// 解析网络信息（从docker inspect获取）
			container.Network = getContainerNetwork(container.ID)

			containers = append(containers, container)
		}
	}

	return containers
}

// parseStatus 解析容器状态
func parseStatus(statusStr string) string {
	statusStr = strings.ToLower(statusStr)
	if strings.Contains(statusStr, "up") {
		return "running"
	} else if strings.Contains(statusStr, "exited") {
		return "stopped"
	} else if strings.Contains(statusStr, "paused") {
		return "paused"
	}
	return "unknown"
}

// parseCreatedTime 解析创建时间
func parseCreatedTime(fields []string) string {
	// 简化处理，返回当前时间格式
	return time.Now().Format("2006-01-02 15:04:05")
}

// getContainerNetwork 获取容器网络信息
func getContainerNetwork(containerID string) string {
	cmd := exec.Command("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return "-"
	}
	ip := strings.TrimSpace(string(output))
	if ip == "" {
		return "-"
	}
	return ip
}

// getContainerStats 获取容器资源使用情况
func getContainerStats(containerID string) *DockerStatsInfo {
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var stats DockerStatsInfo
	err = json.Unmarshal(output, &stats)
	if err != nil {
		return nil
	}

	return &stats
}

// GetContainerDetail 获取容器详情
func GetContainerDetail(c *gin.Context) {
	containerID := c.Param("id")
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "容器ID不能为空",
			"data": nil,
		})
		return
	}

	// 执行docker inspect命令获取容器详情
	cmd := exec.Command("docker", "inspect", containerID)
	output, err := cmd.Output()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "获取容器详情失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	// 解析JSON输出
	var inspectResult []map[string]interface{}
	err = json.Unmarshal(output, &inspectResult)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "解析容器详情失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	if len(inspectResult) == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"code": 1,
			"msg":  "容器不存在",
			"data": nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "success",
		"data": inspectResult[0],
	})
}

// StartContainer 启动容器
func StartContainer(c *gin.Context) {
	containerID := c.Param("id")
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "容器ID不能为空",
			"data": nil,
		})
		return
	}

	cmd := exec.Command("docker", "start", containerID)
	err := cmd.Run()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "启动容器失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "容器启动成功",
		"data": nil,
	})
}

// StopContainer 停止容器
func StopContainer(c *gin.Context) {
	containerID := c.Param("id")
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "容器ID不能为空",
			"data": nil,
		})
		return
	}

	cmd := exec.Command("docker", "stop", containerID)
	err := cmd.Run()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "停止容器失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "容器停止成功",
		"data": nil,
	})
}

// RestartContainer 重启容器
func RestartContainer(c *gin.Context) {
	containerID := c.Param("id")
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "容器ID不能为空",
			"data": nil,
		})
		return
	}

	cmd := exec.Command("docker", "restart", containerID)
	err := cmd.Run()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "重启容器失败: " + err.Error(),
			"data": nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "容器重启成功",
		"data": nil,
	})
}

// CreateContainerSSHSession 为容器创建SSH会话
func CreateContainerSSHSession(c *gin.Context) {
	var req struct {
		ContainerID   string `json:"container_id" binding:"required"`
		ContainerName string `json:"container_name"`
		User          string `json:"user"`
		Shell         string `json:"shell"`
		PtyType       string `json:"pty_type"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "参数错误: " + err.Error(),
			"data": nil,
		})
		return
	}

	// 检查容器是否存在且运行中
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", req.ContainerID)
	output, err := cmd.Output()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "容器不存在或无法访问",
			"data": nil,
		})
		return
	}

	isRunning := strings.TrimSpace(string(output))
	if isRunning != "true" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "容器未运行，无法建立SSH连接",
			"data": nil,
		})
		return
	}

	// 生成会话ID
	sessionID := fmt.Sprintf("container_%s_%d", req.ContainerID, time.Now().UnixNano())

	// 这里可以将会话信息存储到数据库或内存中
	// 暂时直接返回会话ID

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "SSH会话创建成功",
		"data": sessionID,
	})
}

// ContainerSSHConn 容器SSH连接处理
func ContainerSSHConn(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "会话ID不能为空",
		})
		return
	}

	// 从会话ID中提取容器ID
	parts := strings.Split(sessionID, "_")
	if len(parts) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  "无效的会话ID",
		})
		return
	}
	containerID := parts[1]

	// 获取终端大小参数
	widthStr := c.DefaultQuery("w", "80")
	heightStr := c.DefaultQuery("h", "24")

	width, _ := strconv.Atoi(widthStr)
	height, _ := strconv.Atoi(heightStr)

	// 升级为WebSocket连接
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 创建容器exec会话
	execCmd := exec.Command("docker", "exec", "-it", containerID, "/bin/bash")

	// 这里需要实现类似ssh_conn.go中的逻辑
	// 将docker exec的输入输出与WebSocket连接
	handleContainerExec(conn, execCmd, width, height)
}

// handleContainerExec 处理容器exec连接
func handleContainerExec(conn *websocket.Conn, cmd *exec.Cmd, width, height int) {
	// 这里需要实现具体的容器exec处理逻辑
	// 类似于ssh_conn.go中的处理方式
	// 由于篇幅限制，这里提供基本框架

	// 1. 设置PTY
	// 2. 启动命令
	// 3. 处理WebSocket消息
	// 4. 转发输入输出

	// 发送欢迎消息
	welcomeMsg := fmt.Sprintf("欢迎连接到容器终端\r\n")
	conn.WriteMessage(websocket.TextMessage, []byte(welcomeMsg))

	// 这里需要实现完整的终端处理逻辑
}
