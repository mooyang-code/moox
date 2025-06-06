import type { MockMethod } from "vite-plugin-mock";
import { resultSuccess } from "../_utils";

const objectListData = [
  {
    key: "1",
    objectName: "user_info",
    objectType: 1,
    objectSchema: "public",
    description: "用户信息表",
    recordCount: 1250,
    status: 1,
    createTime: "2024-01-15 10:30:00"
  },
  {
    key: "2", 
    objectName: "order_detail",
    objectType: 1,
    objectSchema: "public",
    description: "订单详情表",
    recordCount: 8900,
    status: 1,
    createTime: "2024-01-16 14:20:00"
  },
  {
    key: "3",
    objectName: "v_user_orders",
    objectType: 2,
    objectSchema: "public", 
    description: "用户订单视图",
    recordCount: 0,
    status: 1,
    createTime: "2024-01-17 09:15:00"
  },
  {
    key: "4",
    objectName: "product_catalog",
    objectType: 1,
    objectSchema: "public",
    description: "产品目录表",
    recordCount: 450,
    status: 1,
    createTime: "2024-01-18 11:20:00"
  },
  {
    key: "5",
    objectName: "sp_update_inventory",
    objectType: 3,
    objectSchema: "public",
    description: "更新库存存储过程",
    recordCount: 0,
    status: 2,
    createTime: "2024-01-19 16:45:00"
  }
];

const dataListData = [
  {
    key: "1",
    dataId: "D001",
    dataName: "用户基础信息",
    dataType: 1,
    dataSize: "128KB",
    status: 1,
    createTime: "2024-01-15 10:30:00",
    updateTime: "2024-01-20 14:20:00"
  },
  {
    key: "2",
    dataId: "D002", 
    dataName: "订单金额统计",
    dataType: 2,
    dataSize: "256KB",
    status: 1,
    createTime: "2024-01-16 14:20:00",
    updateTime: "2024-01-21 09:15:00"
  },
  {
    key: "3",
    dataId: "D003",
    dataName: "登录时间记录",
    dataType: 3,
    dataSize: "64KB",
    status: 1,
    createTime: "2024-01-17 09:15:00",
    updateTime: "2024-01-22 16:30:00"
  },
  {
    key: "4",
    dataId: "D004",
    dataName: "用户偏好设置",
    dataType: 4,
    dataSize: "32KB",
    status: 1,
    createTime: "2024-01-18 12:10:00",
    updateTime: "2024-01-23 08:40:00"
  },
  {
    key: "5",
    dataId: "D005",
    dataName: "系统配置参数",
    dataType: 1,
    dataSize: "96KB",
    status: 2,
    createTime: "2024-01-19 15:30:00",
    updateTime: "2024-01-24 10:15:00"
  }
];

export default [
  {
    url: "/mock/data/object-list",
    method: "get",
    timeout: 500,
    response: () => {
      let data = {
        list: objectListData,
        total: objectListData.length
      };
      return resultSuccess(data);
    }
  },
  {
    url: "/mock/data/data-list",
    method: "get", 
    timeout: 500,
    response: () => {
      let data = {
        list: dataListData,
        total: dataListData.length
      };
      return resultSuccess(data);
    }
  },
  {
    url: new RegExp("/mock/data/project/.+/object-list"),
    method: "get",
    timeout: 500,
    response: () => {
      let data = {
        list: objectListData,
        total: objectListData.length
      };
      return resultSuccess(data);
    }
  },
  {
    url: new RegExp("/mock/data/project/.+/data-list"),
    method: "get",
    timeout: 500,
    response: () => {
      let data = {
        list: dataListData,
        total: dataListData.length
      };
      return resultSuccess(data);
    }
  }
] as MockMethod[]; 