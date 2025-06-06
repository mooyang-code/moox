import axios from "@/api";

// 获取对象列表数据
export const getObjectListAPI = () => {
  return axios({
    url: "/mock/data/object-list",
    method: "get"
  });
};

// 获取数据列表数据
export const getDataListAPI = () => {
  return axios({
    url: "/mock/data/data-list",
    method: "get"
  });
};

// 根据项目ID获取对象列表
export const getObjectListByProjectAPI = (projectId: string) => {
  return axios({
    url: `/mock/data/project/${projectId}/object-list`,
    method: "get"
  });
};

// 根据项目ID获取数据列表
export const getDataListByProjectAPI = (projectId: string) => {
  return axios({
    url: `/mock/data/project/${projectId}/data-list`,
    method: "get"
  });
}; 