import { Message } from '@arco-design/web-vue';
import { api } from '@/api/config';
import router from '@/router';

// 任务状态枚举
export enum TaskStatus {
  PROCESSING = 1,  // 处理中
  SUCCESS = 2,     // 成功
  FAILED = 3,      // 失败
  PARTIAL = 4      // 部分成功
}

// 任务详情项
export interface TaskDetailItem {
  item_id: string;
  item_name: string;
  status: number;
  error_message?: string;
}

// 任务状态响应接口
export interface TaskStatusResponse {
  task_id: string;
  task_type: string;
  task_status: TaskStatus;
  total_count: number;
  success_count: number;
  failed_count: number;
  progress: number;
  error_message?: string;
  created_at: string;
  completed_time?: string;
  failed_items?: TaskDetailItem[];
}

// 异步任务管理器
export class AsyncTaskManager {
  private pollingInterval: number | null = null;
  private loadingInstance: any = null;

  /**
   * 生成任务ID（UUID v4）
   */
  static generateTaskId(): string {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
      const r = Math.random() * 16 | 0;
      const v = c === 'x' ? r : (r & 0x3 | 0x8);
      return v.toString(16);
    });
  }

  /**
   * 从URL获取任务ID
   */
  static getTaskIdFromUrl(): string | null {
    // 使用 Vue Router 获取查询参数
    return router.currentRoute.value.query.task_id as string | null;
  }

  /**
   * 设置任务ID到URL
   */
  static setTaskIdToUrl(taskId: string): void {
    // 使用 Vue Router 更新查询参数
    router.replace({
      query: {
        ...router.currentRoute.value.query,
        task_id: taskId
      }
    });
  }

  /**
   * 从URL移除任务ID
   */
  static removeTaskIdFromUrl(): void {
    // 使用 Vue Router 移除查询参数
    const query = { ...router.currentRoute.value.query };
    delete query.task_id;
    router.replace({ query });
  }

  /**
   * 创建异步任务
   */
  async createAsyncTask(taskType: string, requestParams: any): Promise<string> {
    try {
      // 调用新的异步任务创建接口 - 使用Job-Task模型
      const response = await api.post('/asynctask/CreateAsyncJob', {
        tasks: [{
          task_type: taskType,
          request_params: requestParams
        }]
      }, {
        timeout: 20000 // 20秒超时
      });
      
      // 检查响应状态
      if (response.data?.code !== 200) {
        const errorMsg = response.data?.message || '创建任务失败';
        throw new Error(errorMsg);
      }
      
      // 从后台响应中获取job_id
      // 处理数组格式的响应：response.data.data 可能是数组
      let jobData = response.data?.data;
      if (Array.isArray(jobData) && jobData.length > 0) {
        jobData = jobData[0]; // 取数组第一个元素
      }
      
      // 新接口返回的是job_id，我们需要将其作为task_id使用（保持前端兼容性）
      const jobId = jobData?.job_id;
      if (!jobId) {
        throw new Error('服务器未返回job_id');
      }
      
      // 设置到URL
      AsyncTaskManager.setTaskIdToUrl(jobId);
      
      return jobId;
    } catch (error: any) {
      Message.error(error.message || '创建任务失败');
      throw error;
    }
  }

  /**
   * 创建多个独立任务的异步任务
   */
  async createMultipleAsyncTasks(tasks: Array<{taskType: string, requestParams: any}>): Promise<string> {
    try {
      // 调用新的异步任务创建接口 - 使用Job-Task模型
      const response = await api.post('/asynctask/CreateAsyncJob', {
        tasks: tasks.map(task => ({
          task_type: task.taskType,
          request_params: task.requestParams
        }))
      }, {
        timeout: 20000 // 20秒超时
      });
      
      // 检查响应状态
      if (response.data?.code !== 200) {
        const errorMsg = response.data?.message || '创建任务失败';
        throw new Error(errorMsg);
      }
      
      // 从后台响应中获取job_id
      // 处理数组格式的响应：response.data.data 可能是数组
      let jobData = response.data?.data;
      if (Array.isArray(jobData) && jobData.length > 0) {
        jobData = jobData[0]; // 取数组第一个元素
      }
      
      // 新接口返回的是job_id，我们需要将其作为task_id使用（保持前端兼容性）
      const jobId = jobData?.job_id;
      if (!jobId) {
        throw new Error('服务器未返回job_id');
      }
      
      // 设置到URL
      AsyncTaskManager.setTaskIdToUrl(jobId);
      
      return jobId;
    } catch (error: any) {
      Message.error(error.message || '创建任务失败');
      throw error;
    }
  }

  /**
   * 查询任务状态
   * @param taskId 任务ID
   * @param silent 是否静默模式（不弹出错误提示），用于轮询场景
   */
  async queryTaskStatus(taskId: string, silent: boolean = false): Promise<TaskStatusResponse> {
    try {
      // 调用新的异步任务查询接口 - 使用Job-Task模型
      const response = await api.post('/asynctask/QueryAsyncJob', {
        job_id: taskId
      });

      // 检查响应状态
      if (response.data?.code !== 200) {
        const errorMsg = response.data?.message || '查询任务状态失败';
        throw new Error(errorMsg);
      }

      // 从后台响应中获取任务数据
      // 处理数组格式的响应：response.data.data 可能是数组
      let jobData = response.data?.data;
      if (Array.isArray(jobData) && jobData.length > 0) {
        jobData = jobData[0]; // 取数组第一个元素
      }

      // 将Job数据转换为TaskStatusResponse格式（保持前端兼容性）
      const taskStatus: TaskStatusResponse = {
        task_id: jobData?.job_id || taskId,
        task_type: jobData?.tasks?.[0]?.task_type || 'UNKNOWN',
        task_status: this.mapJobStatusToTaskStatus(jobData?.job_status),
        total_count: jobData?.total_task_cnt || 0,
        success_count: jobData?.success_task_cnt || 0,
        failed_count: jobData?.failed_task_cnt || 0,
        progress: jobData?.progress || 0,
        error_message: jobData?.tasks?.[0]?.error_message,
        created_at: jobData?.created_at || new Date().toISOString(),
        completed_time: jobData?.updated_at,
        failed_items: this.extractFailedItems(jobData?.tasks)
      };

      console.log('queryTaskStatus: jobData:', jobData);
      console.log('queryTaskStatus: taskStatus:', taskStatus);

      // 不抛出错误，让调用方处理失败状态
      return taskStatus;
    } catch (error: any) {
      // 静默模式下不弹出错误提示，用于轮询场景（超时或网络问题时继续重试）
      if (!silent) {
        Message.error(error.message || '查询任务状态失败');
      }
      throw error;
    }
  }

  /**
   * 将Job状态映射为Task状态
   */
  private mapJobStatusToTaskStatus(jobStatus: number): TaskStatus {
    // 根据后台Job状态映射到前端Task状态
    // 后台状态: 0-待处理, 1-处理中, 2-成功, 3-失败, 4-部分成功
    switch (jobStatus) {
      case 0: // 待处理
        return TaskStatus.PROCESSING; // 前端显示为处理中
      case 1: // 处理中
        return TaskStatus.PROCESSING;
      case 2: // 成功
        return TaskStatus.SUCCESS;
      case 3: // 失败
        return TaskStatus.FAILED;
      case 4: // 部分成功
        return TaskStatus.PARTIAL;
      default:
        return TaskStatus.PROCESSING;
    }
  }

  /**
   * 从任务列表中提取失败项
   */
  private extractFailedItems(tasks: any[]): TaskDetailItem[] {
    if (!tasks || !Array.isArray(tasks)) {
      console.log('extractFailedItems: tasks is not array or empty');
      return [];
    }
    
    console.log('extractFailedItems: processing tasks:', tasks);
    const failedTasks = tasks.filter(task => task.task_status === 3); // 失败状态
    console.log('extractFailedItems: failed tasks:', failedTasks);
    
    const result = failedTasks.map(task => ({
      item_id: task.task_id,
      item_name: task.task_type,
      status: task.task_status,
      error_message: task.error_message
    }));
    
    console.log('extractFailedItems: result:', result);
    return result;
  }

  /**
   * 开始轮询任务状态
   */
  startPolling(
    taskId: string,
    options: {
      onProgress?: (data: TaskStatusResponse) => void;
      onSuccess?: (data: TaskStatusResponse) => void;
      onFailed?: (data: TaskStatusResponse) => void;
      onPartialSuccess?: (data: TaskStatusResponse) => void;
      interval?: number;
      showLoading?: boolean;
    } = {}
  ): void {
    const {
      onProgress,
      onSuccess,
      onFailed,
      onPartialSuccess,
      interval = 2000,
      showLoading = true
    } = options;

    // 显示loading
    if (showLoading) {
      this.loadingInstance = Message.loading({
        content: '任务执行中，请稍候...',
        duration: 0
      });
    }

    // 立即查询一次
    this.pollOnce(taskId, onProgress, onSuccess, onFailed, onPartialSuccess);

    // 定时轮询
    this.pollingInterval = window.setInterval(() => {
      this.pollOnce(taskId, onProgress, onSuccess, onFailed, onPartialSuccess);
    }, interval);
  }

  /**
   * 停止轮询
   */
  stopPolling(): void {
    if (this.pollingInterval) {
      clearInterval(this.pollingInterval);
      this.pollingInterval = null;
    }
    
    if (this.loadingInstance) {
      this.loadingInstance.close();
      this.loadingInstance = null;
    }
  }

  /**
   * 单次轮询
   */
  private async pollOnce(
    taskId: string,
    onProgress?: (data: TaskStatusResponse) => void,
    onSuccess?: (data: TaskStatusResponse) => void,
    onFailed?: (data: TaskStatusResponse) => void,
    onPartialSuccess?: (data: TaskStatusResponse) => void
  ): Promise<void> {
    try {
      // 使用静默模式查询，超时或请求失败时不弹窗，继续轮询
      const taskStatus = await this.queryTaskStatus(taskId, true);

      // 处理进度回调
      if (onProgress) {
        onProgress(taskStatus);
      }

      // 根据任务状态处理
      switch (taskStatus.task_status) {
        case TaskStatus.PROCESSING:
          // 任务还在处理中，继续轮询
          break;

        case TaskStatus.SUCCESS:
          this.stopPolling();
          if (onSuccess) {
            onSuccess(taskStatus);
          }
          break;

        case TaskStatus.FAILED:
          this.stopPolling();
          if (onFailed) {
            onFailed(taskStatus);
          }
          break;

        case TaskStatus.PARTIAL:
          this.stopPolling();
          if (onPartialSuccess) {
            onPartialSuccess(taskStatus);
          }
          break;
      }
    } catch (error) {
      // 查询失败时（如超时、网络问题），静默处理，继续轮询
      console.warn('轮询任务状态失败，将继续重试:', error);
    }
  }

  /**
   * 检查并恢复页面任务状态
   */
  async checkAndRestoreTask(
    onTaskFound?: (taskId: string, status: TaskStatusResponse) => void
  ): Promise<void> {
    const taskId = AsyncTaskManager.getTaskIdFromUrl();
    
    if (!taskId) {
      return;
    }
    
    try {
      const taskStatus = await this.queryTaskStatus(taskId);
      
      // 处理不同的任务状态
      if (taskStatus.task_status === TaskStatus.PROCESSING) {
        Message.info('任务提交成功，后台执行中，请稍后...');
        
        if (onTaskFound) {
          onTaskFound(taskId, taskStatus);
        }
      } else {
        // 任务已完成，需要显示结果
        // 清除URL中的任务ID
        AsyncTaskManager.removeTaskIdFromUrl();
        
        // 通知调用方任务已完成，需要显示结果
        if (onTaskFound) {
          onTaskFound(taskId, taskStatus);
        }
      }
    } catch (error) {
      // 查询失败，可能任务不存在，清除URL中的任务ID
      AsyncTaskManager.removeTaskIdFromUrl();
    }
  }
}

// 导出单例实例
export const asyncTaskManager = new AsyncTaskManager();
