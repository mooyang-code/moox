import { callControl } from "@/api/admin/http";
export { withOptionalSpace } from "@/api/space-context";

export interface CloudAccountSummary {
  id?: number;
  account_id: string;
  account_name: string;
  provider: string;
  credential_secret_id: string;
  app_id: string;
  cos_region: string;
  cos_bucket: string;
  extra_config: string;
  is_deleted?: boolean;
  create_time?: string;
  modify_time?: string;
}

export interface CloudAccountInput {
  account_id: string;
  account_name: string;
  provider: string;
  credential_secret_id: string;
  app_id: string;
  cos_region: string;
  cos_bucket: string;
  extra_config?: string;
}

export type CloudAccount = CloudAccountSummary;

export const getCloudAccountList = async (): Promise<CloudAccountSummary[]> => {
  const rsp = await callControl<Record<string, never>, { accounts?: CloudAccountSummary[] }>(
    "cloudnode",
    "ListCloudAccounts",
    {}
  );
  return rsp.accounts ?? [];
};

export const createCloudAccount = async (account: CloudAccountInput): Promise<CloudAccountSummary> => {
  const rsp = await callControl<{ account: CloudAccountInput }, { account?: CloudAccountSummary }>(
    "cloudnode",
    "CreateCloudAccount",
    { account }
  );
  return rsp.account as CloudAccountSummary;
};

export const updateCloudAccount = async (account_id: string, account: Partial<CloudAccountInput>): Promise<void> => {
  await callControl<{ account: Partial<CloudAccountInput> }, Record<string, never>>("cloudnode", "UpdateCloudAccount", {
    account: { account_id, ...account }
  });
};

export const deleteCloudAccount = async (accountId: string): Promise<void> => {
  await callControl<{ account_id: string }, Record<string, never>>("cloudnode", "DeleteCloudAccount", {
    account_id: accountId
  });
};
