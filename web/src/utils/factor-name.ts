/**
 * 因子成员键、结果列名、任务相关 ID 的拼接、解析与校验。
 * 与 Go 的 packages/factorname 是同一套规则，两边共用 packages/factorname/testdata/vectors.json。
 *
 * - 因子 ID、输出名、参数名都是小写，名字内部用单下划线分词；段与段之间用双下划线。
 * - 成员键 = 因子 ID [ "__" 参数段 ]，参数段是 "var"（可变成员）或按定义声明顺序写出的参数值。
 * - 列名 = 成员键 [ "__" 输出名 ]，单输出因子没有输出段。
 */

export const MAX_FACTOR_ID_LEN = 24;
export const MAX_OUTPUT_LEN = 16;
export const MAX_PARAM_NAME_LEN = 16;
export const MAX_MEMBER_KEY_LEN = 44;
export const MAX_COLUMN_LEN = 64;
export const MAX_SUFFIX_LEN = 35;

export const SEP = "__";
export const VAR_SEGMENT = "var";
export const PARAMS_COLUMN = "__params";
export const KIND_FIXED = "fixed";
export const KIND_VAR = "var";

export type MemberKind = typeof KIND_FIXED | typeof KIND_VAR;

/** 因子定义声明的一个参数，顺序就是定义里的声明顺序。 */
export interface FactorParamDecl {
  name: string;
  default: number;
}

/** 从成员键或列名解析出的、名字里本来就有的信息。 */
export interface ParsedName {
  factorId: string;
  kind: MemberKind;
  /** 固定成员的名字里带参数段；为 false 的固定成员参数全为默认值，具体取值要查列元数据或因子定义。 */
  hasParams: boolean;
  values: number[];
  /** 为空表示单输出因子。 */
  output: string;
}

export class FactorNameError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "FactorNameError";
  }
}

const NAME_PATTERN = /^[a-z][a-z0-9]*(_[a-z0-9]+)*$/;
const VALUE_PATTERN = /^n?[0-9]+(p[0-9]+)?$/;
const SUFFIX_PATTERN = /^[a-z0-9]([a-z0-9_]*[a-z0-9])?$/;
const SUFFIX_ALPHABET = "abcdefghijklmnopqrstuvwxyz0123456789";
const GENERATED_SUFFIX_LEN = 8;

function validateName(what: string, name: string, max: number): void {
  if (name === "") throw new FactorNameError(`${what}不能为空`);
  if (name.length > max) throw new FactorNameError(`${what} ${name} 超过 ${max} 个字符`);
  if (!NAME_PATTERN.test(name)) {
    throw new FactorNameError(`${what} ${name} 不合法：只能是小写字母开头、由小写字母和数字组成，词与词之间用单个下划线`);
  }
}

export function validateFactorId(id: string): void {
  validateName("因子 ID", id, MAX_FACTOR_ID_LEN);
}

export function validateParamName(name: string): void {
  validateName("参数名", name, MAX_PARAM_NAME_LEN);
}

/** 参数段：var，或用单下划线连接的参数值写法。 */
function isParamSegment(segment: string): boolean {
  if (segment === VAR_SEGMENT) return true;
  if (segment === "") return false;
  return segment.split("_").every(part => VALUE_PATTERN.test(part));
}

/** 多输出因子的输出名：不能是 var、不能长得像参数段、不能等于因子 ID（factorId 为空时跳过这一条）。 */
export function validateOutputName(factorId: string, name: string): void {
  validateName("输出名", name, MAX_OUTPUT_LEN);
  if (isParamSegment(name)) throw new FactorNameError(`输出名 ${name} 与参数段的写法冲突`);
  if (factorId !== "" && name === factorId) throw new FactorNameError(`多输出因子的输出名 ${name} 不能等于因子 ID`);
}

/** 一个因子的全部输出：单输出时输出名就是因子 ID；多输出时至少两项、互不相同、每项都合法。 */
export function validateOutputs(factorId: string, outputs: string[]): void {
  validateFactorId(factorId);
  if (outputs.length === 0) throw new FactorNameError(`因子 ${factorId} 至少要有一个输出`);
  if (outputs.length === 1) {
    if (outputs[0] !== factorId) {
      throw new FactorNameError(
        `因子 ${factorId} 只有一个输出时不写输出名（输出名就是因子 ID），实际是 ${outputs[0]}；有多个输出才需要输出名`
      );
    }
    return;
  }
  const seen = new Set<string>();
  for (const name of outputs) {
    validateOutputName(factorId, name);
    if (seen.has(name)) throw new FactorNameError(`因子 ${factorId} 的输出名 ${name} 重复`);
    seen.add(name);
  }
}

/** 把 1e-7、1.5e+21 这样的指数形式展开成十进制文本。 */
function expandExponent(text: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?e([+-]?\d+)$/i.exec(text);
  if (!match) return text;
  const [, sign, integer, fraction = "", exponent] = match;
  const digits = integer + fraction;
  const point = integer.length + Number(exponent);
  let body: string;
  if (point <= 0) body = `0.${"0".repeat(-point)}${digits}`;
  else if (point >= digits.length) body = digits + "0".repeat(point - digits.length);
  else body = `${digits.slice(0, point)}.${digits.slice(point)}`;
  return sign + body;
}

/** 十进制文本（不出现指数）；-0 按 0。String() 给出最短的往返表示，与 Go 的 FormatFloat(v, 'f', -1, 64) 的数字一致。 */
function decimalText(value: number): string {
  if (!Number.isFinite(value)) throw new FactorNameError(`参数值 ${value} 不是有限数`);
  const normalized = Object.is(value, -0) ? 0 : value;
  const text = String(normalized);
  return /e/i.test(text) ? expandExponent(text) : text;
}

/** 参数值写成成员键里的片段：小数点换成 p、负号换成 n（0.5 → 0p5，-1 → n1）。 */
export function formatValue(value: number): string {
  return decimalText(value).replace(/\./g, "p").replace(/-/g, "n");
}

/** 解析 formatValue 生成的片段，并重新格式化比对：01、1p0、n0 这类等价但不规范的写法一律拒绝。 */
export function parseValue(text: string): number {
  if (!VALUE_PATTERN.test(text)) throw new FactorNameError(`参数值片段 ${text} 不合法`);
  const value = Number(text.replace(/p/g, ".").replace(/n/g, "-"));
  if (!Number.isFinite(value)) throw new FactorNameError(`参数值片段 ${text} 超出范围`);
  const back = formatValue(value);
  if (back !== text) throw new FactorNameError(`参数值片段 ${text} 不是规范写法（应写成 ${back}）`);
  return value;
}

/**
 * 固定成员的成员键：参数全为默认值（或因子没有参数）时就是因子 ID，否则是 因子ID__值1_值2…，参数值按声明顺序全部写出。
 * given 里没给的参数取默认值，给了定义没有的参数名会报错。
 */
export function fixedKey(factorId: string, decl: FactorParamDecl[], given: Record<string, number> = {}): string {
  validateFactorId(factorId);
  const declared = new Set<string>();
  for (const param of decl) {
    validateParamName(param.name);
    declared.add(param.name);
  }
  for (const name of Object.keys(given)) {
    if (!declared.has(name)) throw new FactorNameError(`因子 ${factorId} 没有参数 ${name}`);
  }
  let allDefault = true;
  const parts: string[] = [];
  for (const param of decl) {
    const value = Object.prototype.hasOwnProperty.call(given, param.name) ? given[param.name] : param.default;
    if (value !== param.default) allDefault = false;
    parts.push(formatValue(value));
  }
  if (allDefault) return factorId;
  const key = factorId + SEP + parts.join("_");
  if (key.length > MAX_MEMBER_KEY_LEN) throw new FactorNameError(`按参数生成的成员键 ${key} 超过 ${MAX_MEMBER_KEY_LEN} 个字符`);
  return key;
}

/** 可变成员的成员键：因子ID__var。 */
export function varKey(factorId: string): string {
  validateFactorId(factorId);
  return factorId + SEP + VAR_SEGMENT;
}

/** 结果列名：output 为空表示单输出因子，列名就是成员键；否则是 成员键__输出名。 */
export function column(memberKey: string, output = ""): string {
  parseMemberKey(memberKey);
  if (output === "") return memberKey;
  validateOutputName("", output);
  const name = memberKey + SEP + output;
  if (name.length > MAX_COLUMN_LEN) throw new FactorNameError(`列名 ${name} 超过 ${MAX_COLUMN_LEN} 个字符`);
  return name;
}

/** 一个成员的全部结果列名，顺序与 outputs 一致。单输出因子（outputs 只有因子 ID 本身）得到一个列，列名就是成员键。 */
export function columns(factorId: string, memberKey: string, outputs: string[]): string[] {
  validateOutputs(factorId, outputs);
  if (outputs.length === 1) return [column(memberKey)];
  return outputs.map(output => column(memberKey, output));
}

/** 解析成员键（不含输出段）。 */
export function parseMemberKey(key: string): ParsedName {
  return parse(key, false);
}

/** 解析结果列名。__params 不是因子值列，调用方要先用 PARAMS_COLUMN 判断。 */
export function parseColumn(name: string): ParsedName {
  return parse(name, true);
}

function parse(text: string, allowOutput: boolean): ParsedName {
  const parts = text.split(SEP);
  validateFactorId(parts[0]);
  const parsed: ParsedName = { factorId: parts[0], kind: KIND_FIXED, hasParams: false, values: [], output: "" };
  let rest = parts.slice(1);
  if (rest.length > 2 || (rest.length === 2 && !allowOutput)) throw new FactorNameError(`${text} 的段数太多`);
  let key = parsed.factorId;
  if (rest.length >= 1 && isParamSegment(rest[0])) {
    const segment = rest[0];
    if (segment === VAR_SEGMENT) {
      parsed.kind = KIND_VAR;
    } else {
      parsed.values = segment.split("_").map(parseValue);
      parsed.hasParams = true;
    }
    key += SEP + segment;
    rest = rest.slice(1);
  }
  if (rest.length === 1) {
    if (!allowOutput) throw new FactorNameError(`成员键 ${text} 不能带输出段`);
    validateOutputName(parsed.factorId, rest[0]);
    parsed.output = rest[0];
  } else if (rest.length > 1) {
    throw new FactorNameError(`${text} 的段数太多`);
  }
  if (key.length > MAX_MEMBER_KEY_LEN) throw new FactorNameError(`成员键 ${key} 超过 ${MAX_MEMBER_KEY_LEN} 个字符`);
  if (text.length > MAX_COLUMN_LEN) throw new FactorNameError(`列名 ${text} 超过 ${MAX_COLUMN_LEN} 个字符`);
  return parsed;
}

/** 把解析出的参数值按定义的声明顺序对应到参数名。参数全为默认值的成员得到默认值；可变成员没有参数值，返回 null。 */
export function paramMap(parsed: ParsedName, decl: FactorParamDecl[]): Record<string, number> | null {
  if (parsed.kind === KIND_VAR) return null;
  const out: Record<string, number> = {};
  if (!parsed.hasParams) {
    for (const param of decl) out[param.name] = param.default;
    return out;
  }
  if (parsed.values.length !== decl.length) {
    throw new FactorNameError(
      `因子 ${parsed.factorId} 的成员键带 ${parsed.values.length} 个参数值，定义声明了 ${decl.length} 个参数`
    );
  }
  decl.forEach((param, i) => {
    out[param.name] = parsed.values[i];
  });
  return out;
}

function sortedNames(values: Record<string, number>): string[] {
  const names = Object.keys(values);
  for (const name of names) validateParamName(name);
  return names.sort();
}

function writeValues(values: Record<string, number>): string {
  const items = sortedNames(values).map(name => `"${name}":${decimalText(values[name])}`);
  return `{${items.join(",")}}`;
}

/** 一个成员的参数（参数名 → 取值）的规范 JSON：键按字典序，数字用十进制 JSON 数字，没有空白。 */
export function canonicalValues(values: Record<string, number>): string {
  return writeValues(values);
}

/** __params 列的规范 JSON：成员键 → 参数名 → 取值，两层键都按字典序，没有空白。 */
export function canonicalParams(members: Record<string, Record<string, number>>): string {
  const keys = Object.keys(members);
  for (const key of keys) parseMemberKey(key);
  const items = keys.sort().map(key => `"${key}":${writeValues(members[key])}`);
  return `{${items.join(",")}}`;
}

/** 解析 __params 列的 JSON，并校验成员键和参数名。 */
export function parseParams(text: string): Record<string, Record<string, number>> {
  let members: unknown;
  try {
    members = JSON.parse(text);
  } catch (error) {
    throw new FactorNameError(`__params 不是合法的 JSON：${error instanceof Error ? error.message : String(error)}`);
  }
  if (members === null || typeof members !== "object" || Array.isArray(members))
    throw new FactorNameError("__params 必须是 JSON 对象");
  for (const [key, values] of Object.entries(members as Record<string, unknown>)) {
    parseMemberKey(key);
    if (values === null || typeof values !== "object" || Array.isArray(values))
      throw new FactorNameError(`成员 ${key} 的参数必须是 JSON 对象`);
    for (const name of Object.keys(values as object)) validateParamName(name);
  }
  return members as Record<string, Record<string, number>>;
}

/** 任务 ID 后缀：小写字母、数字和下划线，首尾不能是下划线，最长 35 个字符。 */
export function validateSuffix(suffix: string): void {
  if (suffix.length === 0 || suffix.length > MAX_SUFFIX_LEN || !SUFFIX_PATTERN.test(suffix)) {
    throw new FactorNameError(
      `任务 ID 后缀 ${suffix} 不合法：只能是小写字母、数字和下划线，首尾不能是下划线，最长 ${MAX_SUFFIX_LEN} 个字符`
    );
  }
}

/** 生成 8 位随机后缀（小写字母和数字）。 */
export function newSuffix(): string {
  const raw = new Uint8Array(GENERATED_SUFFIX_LEN);
  globalThis.crypto.getRandomValues(raw);
  return Array.from(raw, b => SUFFIX_ALPHABET[b % SUFFIX_ALPHABET.length]).join("");
}

const TASK_PREFIX = "ftask_";
const DATASET_PREFIX = "dataset_factor_";
const VIEW_PREFIX = "view_factor_";

function withPrefix(prefix: string, suffix: string): string {
  validateSuffix(suffix);
  return prefix + suffix;
}

function trimPrefix(prefix: string, id: string): string {
  if (!id.startsWith(prefix)) throw new FactorNameError(`ID ${id} 不是以 ${prefix} 开头`);
  const suffix = id.slice(prefix.length);
  validateSuffix(suffix);
  return suffix;
}

/** 由同一个后缀生成因子任务、结果数据集、默认 View 的 ID；parse* 是它们的反解，返回后缀。 */
export const taskId = (suffix: string): string => withPrefix(TASK_PREFIX, suffix);
export const resultDatasetId = (suffix: string): string => withPrefix(DATASET_PREFIX, suffix);
export const resultViewId = (suffix: string): string => withPrefix(VIEW_PREFIX, suffix);
export const parseTaskId = (id: string): string => trimPrefix(TASK_PREFIX, id);
export const parseResultDatasetId = (id: string): string => trimPrefix(DATASET_PREFIX, id);
export const parseResultViewId = (id: string): string => trimPrefix(VIEW_PREFIX, id);
