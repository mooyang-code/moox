// CloudNode management batch change status used only for frontend progress display.
export enum BatchChangeStatus {
  PROCESSING = 1,
  SUCCESS = 2,
  FAILED = 3,
  PARTIAL = 4
}

export interface BatchChangeDetailItem {
  item_id: string;
  item_name: string;
  status: number;
  error_message?: string;
}

export interface BatchChangeStatusResponse {
  batch_id: string;
  batch_change_type: string;
  batch_change_status: BatchChangeStatus;
  total_count: number;
  success_count: number;
  failed_count: number;
  progress: number;
  error_message?: string;
  created_at: string;
  completed_time?: string;
  failed_items?: BatchChangeDetailItem[];
}
