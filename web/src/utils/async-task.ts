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
  private taskId: string | null = null;
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
      // 调用异步任务创建接口 - 让后台分配task_id
      const response = await api.post('/collector/AsyncTaskCreate', {
        task_type: taskType,
        request_params: requestParams
      });
      
      // 兼容两种响应格式
      if (response.data?.code !== 200 && response.data?.ret_info?.code !== 200) {
        const errorMsg = response.data?.message || response.data?.ret_info?.msg || '创建任务失败';
        throw new Error(errorMsg);
      }
      
      // 从后台响应中获取task_id
      // 处理数组格式的响应：response.data.data 可能是数组
      let taskData = response.data?.data || response.data?.ret_info?.data;
      if (Array.isArray(taskData) && taskData.length > 0) {
        taskData = taskData[0]; // 取数组第一个元素
      }
      this.taskId = taskData?.task_id;
      if (!this.taskId) {
        throw new Error('服务器未返回task_id');
      }
      
      // 设置到URL
      AsyncTaskManager.setTaskIdToUrl(this.taskId);
      
      return this.taskId;
    } catch (error: any) {
      Message.error(error.message || '创建任务失败');
      throw error;
    }
  }

  /**
   * 查询任务状态
   */
  async queryTaskStatus(taskId: string): Promise<TaskStatusResponse> {
    try {
      const response = await api.post('/collector/AsyncTaskQuery', {
        task_id: taskId
      });
      
      // 兼容两种响应格式
      if (response.data?.code !== 200 && response.data?.ret_info?.code !== 200) {
        const errorMsg = response.data?.message || response.data?.ret_info?.msg || '查询任务状态失败';
        throw new Error(errorMsg);
      }
      
      // 返回数据也要兼容两种格式
      // 处理数组格式的响应：response.data.data 可能是数组
      let taskData = response.data?.data || response.data?.ret_info?.data;
      if (Array.isArray(taskData) && taskData.length > 0) {
        taskData = taskData[0]; // 取数组第一个元素
      }
      return taskData;
    } catch (error: any) {
      Message.error(error.message || '查询任务状态失败');
      throw error;
    }
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

    this.taskId = taskId;

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
      const taskStatus = await this.queryTaskStatus(taskId);
      
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
      console.error('轮询任务状态失败:', error);
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
