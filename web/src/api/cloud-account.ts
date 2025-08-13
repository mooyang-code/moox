import { api } from '@/api/config';

// 云账户接口定义
export interface CloudAccount {
  id?: number;
  account_id: string;
  account_name: string;
  provider: string;
  secret_id: string;
  secret_key: string;
  extra_config: string;
  invalid?: number;
  create_time?: string;
  modify_time?: string;
}

// 获取云账户列表
export const getCloudAccountList = async (): Promise<any> => {
  const response = await api.post('/collector/ListCloudAccounts', {});
  return response.data;
};

// 创建云账户
export const createCloudAccount = async (account: Omit<CloudAccount, 'id' | 'create_time' | 'modify_time' | 'invalid'>) => {
  const response = await api.post('/collector/CreateCloudAccount', account);
  return response;
};

// 更新云账户
export const updateCloudAccount = async (account_id: string, account: Partial<CloudAccount>) => {
  const response = await api.post('/collector/UpdateCloudAccount', {
    account_id,
    ...account
  });
  return response;
};

// 删除云账户
export const deleteCloudAccount = async (accountId: string) => {
  const response = await api.post('/collector/DeleteCloudAccount', {
    account_id: accountId
  });
  return response;
};

// 获取云账户详情
export const getCloudAccountDetail = async (accountId: string): Promise<any> => {
  const response = await api.post('/collector/GetCloudAccount', {
    account_id: accountId
  });
  return response.data;
};

// 根据云厂商获取账户列表
export const getCloudAccountsByProvider = async (provider: string): Promise<any> => {
  const response = await api.post('/collector/ListCloudAccountsByProvider', {
    provider: provider
  });
  return response.data;
};