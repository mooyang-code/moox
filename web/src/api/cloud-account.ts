import { callControl } from "@/api/admin/http";

/** 云账号：credential_secret_id 是密钥管理里类别 cloud、厂商 tencent 的密钥 ID，不是密钥本身。云账号是全局的，不分空间。 */
export interface CloudAccount {
  account_id: string;
  account_name: string;
  /** 只支持 tencent。 */
  provider: string;
  credential_secret_id: string;
  app_id: string;
  cos_region: string;
  cos_bucket: string;
  create_time?: string;
  modify_time?: string;
}

export type CloudAccountInput = Omit<CloudAccount, "create_time" | "modify_time">;

export const getCloudAccountList = async (): Promise<CloudAccount[]> => {
  const rsp = await callControl<Record<string, never>, { accounts?: CloudAccount[] }>("collector", "ListCloudAccounts", {});
  return rsp.accounts ?? [];
};

export const createCloudAccount = async (account: CloudAccountInput): Promise<CloudAccount | undefined> => {
  const rsp = await callControl<{ account: CloudAccountInput }, { account?: CloudAccount }>("collector", "CreateCloudAccount", {
    account
  });
  return rsp.account;
};

/** 整行写入：没填的字段会被清空。 */
export const updateCloudAccount = async (account: CloudAccountInput): Promise<CloudAccount | undefined> => {
  const rsp = await callControl<{ account: CloudAccountInput }, { account?: CloudAccount }>("collector", "UpdateCloudAccount", {
    account
  });
  return rsp.account;
};

/**
 * 账号还被 scf_plan 引用、还有代码包或云上还有它建的函数时，Collector 拒绝删除，错误信息就是原因。
 * 不弹全局提示：调用方自己把原因提示一次，网络出错时也只提示一次。
 */
export const deleteCloudAccount = async (accountId: string): Promise<void> => {
  await callControl<{ account_id: string }, Record<string, never>>(
    "collector",
    "DeleteCloudAccount",
    { account_id: accountId },
    { silentError: true }
  );
};
