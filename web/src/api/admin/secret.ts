import { callControl } from './http';

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
  return callControl<ListSecretsReq, ListSecretsRsp>('secret', 'ListSecrets', req);
}

export function getSecret(secretId: string) {
  return callControl<{ secret_id: string }, { secret: Secret }>('secret', 'GetSecret', { secret_id: secretId });
}

export function createSecret(secret: Secret) {
  return callControl<CreateSecretReq, CreateSecretRsp>('secret', 'CreateSecret', { secret });
}

export function updateSecret(req: UpdateSecretReq) {
  return callControl<UpdateSecretReq, { ret_info?: { code?: number; msg?: string } }>('secret', 'UpdateSecret', req);
}

export function deleteSecret(secretId: string) {
  return callControl<{ secret_id: string }, { ret_info?: { code?: number; msg?: string } }>('secret', 'DeleteSecret', { secret_id: secretId });
}

export function toggleSecretStatus(secretId: string, status: string) {
  return callControl<ToggleSecretStatusReq, { ret_info?: { code?: number; msg?: string } }>('secret', 'ToggleSecretStatus', { secret_id: secretId, status });
}
