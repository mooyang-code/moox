package asynctask

import (
	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// 任务状态常量
const (
	TaskStatusPending    = 0 // 待处理
	TaskStatusProcessing = 1 // 处理中
	TaskStatusRunning    = 1 // 运行中（与处理中相同）
	TaskStatusSuccess    = 2 // 成功
	TaskStatusFailed     = 3 // 失败
	TaskStatusPartial    = 4 // 部分成功
	TaskStatusCancelled  = 5 // 已取消
)

// 任务类型常量
const (
	TaskTypeCreateNode      = "CREATE_NODE"        // 创建节点
	TaskTypeDeleteNode      = "DELETE_NODE"        // 删除节点
	TaskTypeDeployNode      = "DEPLOY_NODE"        // 部署节点
	TaskTypeUploadFileToCOS = "UPLOAD_FILE_TO_COS" // 文件上传到COS
)

// BuildQueryAsyncJobRsp 由 model 构建 PB QueryAsyncJobRsp（service 层一次性 model→PB 转换）。
func BuildQueryAsyncJobRsp(job *model.AsyncJob, tasks []*model.AsyncJobTask) *pb.QueryAsyncJobRsp {
	rsp := &pb.QueryAsyncJobRsp{
		JobId:          job.JobID,
		TotalTaskCnt:   int32(job.TotalTaskCnt),
		SuccessTaskCnt: int32(job.SuccessTaskCnt),
		FailedTaskCnt:  int32(job.FailedTaskCnt),
		IsStarted:      int32(job.IsStarted),
		JobStatus:      int32(job.CalculateStatus()),
		JobStatusText:  job.GetStatusText(),
		Progress:       int32(job.GetProgress()),
		CreatedAt:      job.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:      job.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	if len(tasks) > 0 {
		rsp.Tasks = make([]*pb.TaskQueryResult, len(tasks))
		for i, task := range tasks {
			rsp.Tasks[i] = BuildTaskQueryResult(task)
		}
	}
	return rsp
}

// BuildTaskQueryResult 由 model 构建 PB TaskQueryResult。
func BuildTaskQueryResult(task *model.AsyncJobTask) *pb.TaskQueryResult {
	rsp := &pb.TaskQueryResult{
		TaskId:         task.TaskID,
		JobId:          task.JobID,
		TaskType:       task.TaskType,
		TaskStatus:     int32(task.TaskStatus),
		TaskStatusText: task.GetStatusText(),
		RequestParams:  task.RequestParams,
		ResultData:     task.ResultData,
		ErrorMessage:   task.ErrorMessage,
		CreatedAt:      task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if task.StartedTime != nil {
		rsp.StartedTime = task.StartedTime.Format("2006-01-02 15:04:05")
	}
	if task.CompletedTime != nil {
		rsp.CompletedTime = task.CompletedTime.Format("2006-01-02 15:04:05")
	}
	return rsp
}
