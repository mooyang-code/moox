import { callControl } from '@/api/admin/http';

export interface MarketModule {
  market_id: string;
  space_id: string;
  readiness_status: string;
  runtime_enabled: boolean;
  instrument_types: string[];
}

export interface MarketStatusResponse {
  module?: MarketModule;
  runtime_provider_count: number;
}

export const listMarketModules = async (): Promise<MarketModule[]> => {
  const rsp = await callControl<Record<string, never>, { modules?: MarketModule[] }>('collector', 'ListMarketModules', {});
  return rsp.modules ?? [];
};

export const getMarketStatus = async (marketId: string): Promise<MarketStatusResponse> =>
  callControl<{ market_id: string }, MarketStatusResponse>('collector', 'GetMarketStatus', { market_id: marketId });
