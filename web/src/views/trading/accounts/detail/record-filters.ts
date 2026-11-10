import { computed, inject, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import type { MarketType } from "@/api/trade";
import { isStockOnly } from "../shared/markets";
import { detailContextKey } from "./context";
import { instrumentFilter } from "./instrument-filter";
import { rangeFromQuery, rangeParams, type RecordRange } from "./record-range";

/** 地址栏的数字市场转回 MarketType；不认识的值当作没选。 */
function marketFromQuery(value: unknown): MarketType | undefined {
  // 地址里的值都是字符串，市场要转回数字，否则和可选市场比较不相等、会被重置成第一个
  const market = Number(value);
  return [0, 1, 2, 3].includes(market) ? (market as MarketType) : undefined;
}

/**
 * 订单、成交两个页签共用的筛选：时间范围、执行账户、市场、标的。
 * 筛选条件写在地址里（键名 = prefix + range/account/symbol/market），切换页签回来、刷新页面后仍保持；
 * 页签自己的筛选（订单的状态、来源）经 extraQuery 一起写进地址。
 */
export function useRecordFilters(options: {
  /** 地址栏键名前缀，两个页签不能相同 */
  prefix: string;
  /** 页签自己的筛选写进地址的部分，值为空串或 undefined 时不写 */
  extraQuery?: () => Record<string, string | undefined>;
  /** 查询按钮、回车、筛选变化后重新读取第一页 */
  reload: () => void;
}) {
  const { logical, accounts } = inject(detailContextKey)!;
  const route = useRoute();
  const router = useRouter();
  const key = (name: string) => `${options.prefix}${name}`;
  const queryText = (name: string) =>
    typeof route.query[key(name)] === "string" ? (route.query[key(name)] as string) : undefined;

  const range = ref<RecordRange>(rangeFromQuery(route.query[key("range")]));
  const accountId = ref<string | undefined>(queryText("account"));
  const symbol = ref(queryText("symbol") ?? "");
  // 已提交的币名：输入框打到一半时，轮询、翻页仍按上次查询的币名读，回车或点“查询”才换
  const appliedSymbol = ref(symbol.value.trim());
  const market = ref<MarketType | undefined>(marketFromQuery(queryText("market")));
  const page = ref(1);

  const memberOptions = computed(() =>
    (logical.value.members || []).map(member => ({
      id: member.trading_account_id,
      label: accounts.value.get(member.trading_account_id)?.name || member.trading_account_id
    }))
  );
  const accountName = (id: string) => accounts.value.get(id)?.name || id;
  // 标的筛选按所选市场补全；选了执行账户时只列它开通的市场
  const marketOptions = computed<MarketType[]>(() =>
    accountId.value ? (accounts.value.get(accountId.value)?.markets ?? []) : (logical.value.markets ?? [])
  );
  const stockOnly = computed(() => isStockOnly(marketOptions.value));
  watch(
    marketOptions,
    markets => {
      if (!market.value || !markets.includes(market.value)) market.value = markets[0];
    },
    { immediate: true }
  );

  /** 读订单、成交共用的查询条件：执行账户、标的、时间范围。 */
  function queryParams() {
    return {
      logical_account_id: logical.value.logical_account_id,
      trading_account_id: accountId.value,
      instrument_id: instrumentFilter(appliedSymbol.value, stockOnly.value ? 3 : market.value, logical.value.settlement_asset),
      ...rangeParams(range.value)
    };
  }

  function search() {
    page.value = 1;
    appliedSymbol.value = symbol.value.trim();
    void router.replace({
      query: {
        ...route.query,
        [key("range")]: range.value === "7d" ? undefined : range.value,
        [key("account")]: accountId.value || undefined,
        [key("symbol")]: appliedSymbol.value || undefined,
        [key("market")]: appliedSymbol.value ? market.value : undefined,
        ...Object.fromEntries(Object.entries(options.extraQuery?.() ?? {}).map(([name, value]) => [name, value || undefined]))
      }
    });
    options.reload();
  }

  return {
    range,
    accountId,
    symbol,
    market,
    page,
    memberOptions,
    accountName,
    marketOptions,
    stockOnly,
    queryParams,
    search
  };
}
