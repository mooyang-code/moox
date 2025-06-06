export interface ObjectList {
  key: string;
  objectName: string;
  objectType: number;
  objectSchema: string;
  description: string;
  recordCount: number;
  status: number;
  createTime: string;
}

export interface FormData {
  form: {
    objectName: string;
    objectType: number | null;
    objectSchema: string;
    description: string;
    status: number | null;
  };
  search: boolean;
}

export interface RowSelection {
  type: string;
  showCheckedAll: boolean;
  onlyCurrent: boolean;
}

export interface Pagination {
  showPageSize: boolean;
  showTotal: boolean;
  current: number;
  pageSize: number;
  total: number;
} 