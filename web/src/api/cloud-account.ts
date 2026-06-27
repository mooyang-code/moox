import { api } from '@/api/config';
import { isRetInfoSuccess } from '@/api/ret-info';
export { withOptionalSpace } from '@/api/space-context';

// 云账户接口定义（对应后端 pb.CloudAccount）
export interface CloudAccount {
  id?: number;
  account_id: string;
  account_name: string;
  provider: string;
  secret_id: string;
  secret_key: string;
  app_id: string;
  cos_region: string;
  cos_bucket: string;
  extra_config: string;
  invalid?: number;
  create_time?: string;
  modify_time?: string;
}

// 统一校验 ret_info 并提取业务数据；失败抛错。
function unwrap<T = any>(rsp: any): T {
  if (rsp?.ret_info && !isRetInfoSuccess(rsp.ret_info.code)) {
    throw new Error(rsp.ret_info.msg || '请求失败');
  }
  return rsp as T;
}

// 获取云账户列表
export const getCloudAccountList = async (): Promise<CloudAccount[]> => {
  const response = await api.post('/cloudnode/ListCloudAccounts', {});
  const rsp = unwrap<{ accounts?: CloudAccount[] }>(response.data);
  return rsp.accounts ?? [];
};

// 创建云账户
export const createCloudAccount = async (account: Omit<CloudAccount, 'id' | 'create_time' | 'modify_time' | 'invalid'>): Promise<CloudAccount> => {
  const response = await api.post('/cloudnode/CreateCloudAccount', { account });
  const rsp = unwrap<{ account?: CloudAccount }>(response.data);
  return rsp.account as CloudAccount;
};

// 更新云账户
export const updateCloudAccount = async (account_id: string, account: Partial<CloudAccount>): Promise<void> => {
  const response = await api.post('/cloudnode/UpdateCloudAccount', {
    account: { account_id, ...account }
  });
  unwrap(response.data);
};

// 删除云账户
export const deleteCloudAccount = async (accountId: string): Promise<void> => {
  const response = await api.post('/cloudnode/DeleteCloudAccount', {
    account_id: accountId
  });
  unwrap(response.data);
};

// 获取云账户详情
export const getCloudAccountDetail = async (accountId: string): Promise<CloudAccount | null> => {
  const response = await api.post('/cloudnode/GetCloudAccount', {
    account_id: accountId
  });
  const rsp = unwrap<{ account?: CloudAccount }>(response.data);
  return rsp.account ?? null;
};

// 根据云厂商获取账户列表
export const getCloudAccountsByProvider = async (provider: string): Promise<CloudAccount[]> => {
  const response = await api.post('/cloudnode/ListCloudAccounts', { provider });
  const rsp = unwrap<{ accounts?: CloudAccount[] }>(response.data);
  return rsp.accounts ?? [];
};
