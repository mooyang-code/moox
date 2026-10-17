import { callControl } from "./http";

export interface Secret {
  id?: number;
  secret_id?: string;
  name: string;
  description?: string;
  category: string;
  provider?: string;
  secret_type?: string;
  key_id?: string;
  secret_value?: string;
  extra_config?: string;
  status?: string;
  last_used_at?: string;
  last_used_by?: string;
  creator?: string;
  create_time?: string;
  modify_time?: string;
}

export interface ListSecretsReq {
  keyword?: string;
  category?: string;
  provider?: string;
  status?: string;
  offset?: number;
  limit?: number;
}

export interface ListSecretsRsp {
  secrets: Secret[];
  total: number;
}

export interface CreateSecretReq {
  secret: Secret;
}

export interface CreateSecretRsp {
  secret_id: string;
}

export interface UpdateSecretReq {
  secret_id: string;
  name?: string;
  description?: string;
  key_id?: string;
  secret_value?: string;
  extra_config?: string;
  category?: string;
  provider?: string;
  secret_type?: string;
}

export interface ToggleSecretStatusReq {
  secret_id: string;
  status: string;
}

export function listSecrets(req: ListSecretsReq = {}) {
  return callControl<ListSecretsReq, ListSecretsRsp>("secret", "ListSecrets", req);
}

export function createSecret(secret: Secret) {
  return callControl<CreateSecretReq, CreateSecretRsp>("secret", "CreateSecret", { secret });
}

export function updateSecret(req: UpdateSecretReq) {
  return callControl<UpdateSecretReq, Record<string, never>>("secret", "UpdateSecret", req);
}

export function deleteSecret(secretId: string) {
  return callControl<{ secret_id: string }, Record<string, never>>("secret", "DeleteSecret", { secret_id: secretId });
}

export function toggleSecretStatus(secretId: string, status: string) {
  return callControl<ToggleSecretStatusReq, Record<string, never>>("secret", "ToggleSecretStatus", {
    secret_id: secretId,
    status
  });
}

export interface ExportSecretsRsp {
  bundle: string;
  count: number;
}

export interface ImportSecretsReq {
  bundle: string;
  passphrase: string;
  overwrite?: boolean;
}

export interface ImportSecretsRsp {
  created?: number;
  updated?: number;
  skipped?: string[];
  failed?: string[];
}

/** 导出全部秘钥为口令加密的迁移包（仅全局管理员）。明文只在服务端出现，返回的迁移包里只有密文。 */
export function exportSecrets(passphrase: string) {
  return callControl<{ passphrase: string }, ExportSecretsRsp>("secret", "ExportSecrets", { passphrase });
}

/** 用导出口令导入迁移包（仅全局管理员），按 secret_id 导入；overwrite 为 true 时覆盖同一 secret_id 的已有秘钥。 */
export function importSecrets(req: ImportSecretsReq) {
  return callControl<ImportSecretsReq, ImportSecretsRsp>("secret", "ImportSecrets", req);
}
