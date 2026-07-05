import type { Account, Balance, Instrument, OrderSide, Position } from '@/api/trade/types';

export type LiquidationSourceType = 'spot_balance' | 'spot_dust' | 'swap_position';
export type LiquidationStatus = 'pending' | 'submitting' | 'success' | 'failed';

export interface LiquidationTarget {
  id: string;
  account_id: string;
  channel_id: string;
  market_type: Account['account_type'];
  source_type: LiquidationSourceType;
  source_name: string;
  symbol: string;
  side: OrderSide;
  side_label: string;
  pos_side?: string;
  quantity: string;
  available?: string;
  frozen?: string;
  total?: string;
  reduce_only: boolean;
  note?: string;
}

export interface LiquidationResult {
  status: LiquidationStatus;
  message?: string;
  order_id?: string;
  exchange_order_id?: string;
}

export interface SpotLiquidationPlan {
  targets: LiquidationTarget[];
  hidden_count: number;
}

export interface LiquidationSubmitGroups {
  dust_targets: LiquidationTarget[];
  dust_assets: string[];
  order_targets: LiquidationTarget[];
}

const STABLE_BALANCE_CURRENCIES = new Set([
  'USD', 'USDT', 'USDC', 'FDUSD', 'BUSD', 'TUSD', 'DAI', 'USDP', 'PYUSD', 'USD1', 'BFUSD',
]);

const DUST_TARGET_CURRENCY = 'BNB';

export function buildSpotLiquidationPlan(account: Account, balances: Balance[], instruments: Instrument[] = []): SpotLiquidationPlan {
  const quoteCurrency = normalizeCurrency(account.base_currency) || 'USDT';
  const targets: LiquidationTarget[] = [];
  let hiddenCount = 0;
  balances.forEach((balance) => {
    const currency = normalizeCurrency(balance.currency);
    const quantity = normalizeDecimalText(balance.available || balance.total);
    if (!currency || currency === quoteCurrency || STABLE_BALANCE_CURRENCIES.has(currency) || !isPositiveAmount(quantity)) {
      return;
    }
    const candidateSymbol = `${currency}${quoteCurrency}`;
    const instrument = findInstrument(instruments, candidateSymbol);
    if (instruments.length > 0 && !instrument) {
      const dustTarget = buildSpotDustTarget(account, balance, currency, quantity, `未找到 ${candidateSymbol} 现货交易对，尝试转为 ${DUST_TARGET_CURRENCY}`);
      if (dustTarget) targets.push(dustTarget);
      else hiddenCount += 1;
      return;
    }
    if (instrument && !isTradableInstrument(instrument)) {
      const dustTarget = buildSpotDustTarget(account, balance, currency, quantity, `交易对不可交易，尝试转为 ${DUST_TARGET_CURRENCY}`);
      if (dustTarget) targets.push(dustTarget);
      else hiddenCount += 1;
      return;
    }
    const normalized = normalizeQuantityByInstrument(quantity, instrument);
    if (!normalized.tradeable) {
      const dustTarget = buildSpotDustTarget(account, balance, currency, quantity, `低于交易所最小下单量，尝试转为 ${DUST_TARGET_CURRENCY}`);
      if (dustTarget) targets.push(dustTarget);
      else hiddenCount += 1;
      return;
    }
    if (!meetsMinNotional(normalized.quantity, instrument)) {
      const dustTarget = buildSpotDustTarget(account, balance, currency, quantity, `低于 ${quoteCurrency} 最小成交额，尝试转为 ${DUST_TARGET_CURRENCY}`);
      if (dustTarget) targets.push(dustTarget);
      else hiddenCount += 1;
      return;
    }
    const symbol = instrument?.symbol || candidateSymbol;
    const note = normalized.quantity === quantity
      ? `按 ${quoteCurrency} 市价卖出可用余额`
      : `按 ${quoteCurrency} 市价卖出可用余额（已按交易所步长截断）`;
    targets.push({
      id: `spot:${account.account_id}:${currency}`,
      account_id: account.account_id,
      channel_id: account.channel_id,
      market_type: account.account_type,
      source_type: 'spot_balance',
      source_name: currency,
      symbol,
      side: 1,
      side_label: '卖出',
      quantity: normalized.quantity,
      available: normalizeDecimalText(balance.available),
      frozen: normalizeDecimalText(balance.frozen),
      total: normalizeDecimalText(balance.total),
      reduce_only: false,
      note,
    });
  });
  return { targets: targets.sort((a, b) => a.symbol.localeCompare(b.symbol)), hidden_count: hiddenCount };
}

export function buildSpotLiquidationTargets(account: Account, balances: Balance[], instruments: Instrument[] = []): LiquidationTarget[] {
  return buildSpotLiquidationPlan(account, balances, instruments).targets;
}

export function splitLiquidationTargetsForSubmit(targets: LiquidationTarget[]): LiquidationSubmitGroups {
  const dustTargets: LiquidationTarget[] = [];
  const orderTargets: LiquidationTarget[] = [];
  const dustAssets: string[] = [];
  const seenDustAssets = new Set<string>();
  targets.forEach((target) => {
    if (target.source_type !== 'spot_dust') {
      orderTargets.push(target);
      return;
    }
    dustTargets.push(target);
    const asset = normalizeCurrency(target.source_name);
    if (asset && !seenDustAssets.has(asset)) {
      seenDustAssets.add(asset);
      dustAssets.push(asset);
    }
  });
  return { dust_targets: dustTargets, dust_assets: dustAssets, order_targets: orderTargets };
}

function buildSpotDustTarget(account: Account, balance: Balance, currency: string, quantity: string, note: string): LiquidationTarget | null {
  if (currency === DUST_TARGET_CURRENCY) {
    return null;
  }
  return {
    id: `dust:${account.account_id}:${currency}`,
    account_id: account.account_id,
    channel_id: account.channel_id,
    market_type: account.account_type,
    source_type: 'spot_dust',
    source_name: currency,
    symbol: `${currency}->${DUST_TARGET_CURRENCY}`,
    side: 1,
    side_label: `转${DUST_TARGET_CURRENCY}`,
    quantity,
    available: normalizeDecimalText(balance.available),
    frozen: normalizeDecimalText(balance.frozen),
    total: normalizeDecimalText(balance.total),
    reduce_only: false,
    note,
  };
}

export function buildSwapLiquidationTargets(account: Account, positions: Position[]): LiquidationTarget[] {
  const targets: LiquidationTarget[] = [];
  positions.forEach((position) => {
    const quantity = normalizeDecimalText(absDecimalText(position.quantity));
    if (!position.symbol || !isPositiveAmount(quantity)) {
      return;
    }
    const posSide = normalizePositionSide(position.pos_side, position.quantity);
    const side = closeSide(posSide, position.quantity);
    targets.push({
      id: `swap:${account.account_id}:${position.symbol}:${posSide || 'net'}`,
      account_id: account.account_id,
      channel_id: position.channel_id || account.channel_id,
      market_type: account.account_type,
      source_type: 'swap_position',
      source_name: `${position.symbol}${posSide ? ` ${posSide}` : ''}`,
      symbol: position.symbol,
      side,
      side_label: side === 1 ? '卖出平多' : '买入平空',
      pos_side: posSide,
      quantity,
      total: normalizeDecimalText(position.quantity),
      reduce_only: true,
      note: '市价平仓',
    });
  });
  return targets.sort((a, b) => a.symbol.localeCompare(b.symbol) || (a.pos_side || '').localeCompare(b.pos_side || ''));
}

export function normalizeCurrency(value?: string) {
  return (value || '').trim().toUpperCase();
}

export function isPositiveAmount(value?: string) {
  const parsed = Number.parseFloat((value || '0').trim());
  return Number.isFinite(parsed) && parsed > 0;
}

export function normalizeDecimalText(value?: string) {
  const raw = (value || '0').trim();
  if (!raw) return '0';
  return raw.replace(/^\+/, '');
}

export function absDecimalText(value?: string) {
  const raw = normalizeDecimalText(value);
  return raw.startsWith('-') ? raw.slice(1) : raw;
}

interface QuantityNormalization {
  quantity: string;
  tradeable: boolean;
}

function findInstrument(instruments: Instrument[], symbol: string): Instrument | undefined {
  const want = symbolKey(symbol);
  return instruments.find((item) => symbolKey(item.symbol) === want);
}

function symbolKey(symbol?: string) {
  return (symbol || '').trim().toUpperCase().replace(/[-_/]/g, '');
}

function isTradableInstrument(instrument: Instrument) {
  const status = (instrument.status || '').trim().toUpperCase();
  if (!status) return true;
  return status === 'TRADING' || status === 'LIVE' || status === 'ONLINE';
}

function normalizeQuantityByInstrument(quantity: string, instrument?: Instrument): QuantityNormalization {
  if (!instrument?.lot_size) {
    return { quantity, tradeable: true };
  }
  const normalized = floorDecimalToStep(quantity, instrument.lot_size, instrument.min_qty);
  if (!normalized || !isPositiveAmount(normalized)) {
    return { quantity: '0', tradeable: false };
  }
  return { quantity: normalized, tradeable: true };
}

function meetsMinNotional(quantity: string, instrument?: Instrument): boolean {
  if (!instrument?.min_notional || !instrument.last_price) {
    return true;
  }
  const notional = multiplyDecimalText(quantity, instrument.last_price);
  if (!notional) {
    return false;
  }
  return compareDecimalText(notional, instrument.min_notional) >= 0;
}

function floorDecimalToStep(quantity: string, step: string, minQty?: string): string | null {
  try {
    const scale = Math.max(decimalPlaces(quantity), decimalPlaces(step), decimalPlaces(minQty || '0'));
    const qUnits = decimalToUnits(quantity, scale);
    const stepUnits = decimalToUnits(step, scale);
    if (qUnits <= 0n) return null;
    if (stepUnits <= 0n) return normalizeDecimalText(quantity);
    const adjustedUnits = (qUnits / stepUnits) * stepUnits;
    if (adjustedUnits <= 0n) return null;
    if (minQty && decimalToUnits(minQty, scale) > adjustedUnits) return null;
    return unitsToDecimal(adjustedUnits, scale);
  } catch {
    return null;
  }
}

function multiplyDecimalText(a: string, b: string): string | null {
  try {
    const scaleA = decimalPlaces(a);
    const scaleB = decimalPlaces(b);
    const units = decimalToUnits(a, scaleA) * decimalToUnits(b, scaleB);
    return unitsToDecimal(units, scaleA + scaleB);
  } catch {
    return null;
  }
}

function compareDecimalText(a: string, b: string): number {
  const scale = Math.max(decimalPlaces(a), decimalPlaces(b));
  const left = decimalToUnits(a, scale);
  const right = decimalToUnits(b, scale);
  if (left > right) return 1;
  if (left < right) return -1;
  return 0;
}

function decimalPlaces(value: string) {
  const raw = normalizeDecimalText(value);
  const dot = raw.indexOf('.');
  if (dot < 0) return 0;
  return raw.slice(dot + 1).replace(/0+$/, '').length;
}

function decimalToUnits(value: string, scale: number) {
  const raw = normalizeDecimalText(value);
  const negative = raw.startsWith('-');
  const unsigned = negative ? raw.slice(1) : raw;
  const [wholeRaw, fractionRaw = ''] = unsigned.split('.');
  const whole = wholeRaw || '0';
  const fraction = fractionRaw.padEnd(scale, '0').slice(0, scale);
  const units = BigInt(`${whole}${fraction}` || '0');
  return negative ? -units : units;
}

function unitsToDecimal(units: bigint, scale: number) {
  const negative = units < 0n;
  const absUnits = negative ? -units : units;
  if (scale <= 0) return `${negative ? '-' : ''}${absUnits.toString()}`;
  const factor = 10n ** BigInt(scale);
  const whole = absUnits / factor;
  const fraction = (absUnits % factor).toString().padStart(scale, '0').replace(/0+$/, '');
  return `${negative ? '-' : ''}${whole.toString()}${fraction ? `.${fraction}` : ''}`;
}

function normalizePositionSide(posSide: string | undefined, quantity: string) {
  const normalized = (posSide || '').trim().toUpperCase();
  if (normalized === 'LONG' || normalized === 'SHORT') return normalized;
  const parsed = Number.parseFloat(quantity || '0');
  if (Number.isFinite(parsed) && parsed < 0) return 'SHORT';
  if (Number.isFinite(parsed) && parsed > 0) return 'LONG';
  return normalized === 'BOTH' || normalized === 'NET' ? 'BOTH' : '';
}

function closeSide(posSide: string, quantity: string): OrderSide {
  if (posSide === 'SHORT') return 0;
  if (posSide === 'LONG') return 1;
  const parsed = Number.parseFloat(quantity || '0');
  return Number.isFinite(parsed) && parsed < 0 ? 0 : 1;
}
