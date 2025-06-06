export interface DataList {
  key: string;
  dataId: string;
  dataName: string;
  dataType: number;
  dataSize: string;
  status: number;
  createTime: string;
  updateTime: string;
}

export interface FormData {
  form: {
    dataId: string;
    dataName: string;
    dataType: number | null;
    dataSize: string;
    status: number | null;
    createTime: any;
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

export interface Columns {
  title: string;
  dataIndex: string;
  slotName?: string;
  align?: string;
  checked: boolean;
  fixed?: string;
  width?: string;
}

export interface DensityType {
  label: string;
  value: string;
} 