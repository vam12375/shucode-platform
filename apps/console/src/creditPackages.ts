export type CreditPackageTarget = "personal" | "team";
export type CreditPackageStatus = "draft" | "published" | "archived";

export interface CreditPackage {
  sku: string;
  name: string;
  description: string;
  targetType: CreditPackageTarget;
  amountYuan: string;
  creditAmount: string;
  status: CreditPackageStatus;
  recommended: boolean;
  sortOrder: number;
}

const amountPattern = /^(?:[1-9]\d{0,4}|[1-9]\d{0,4}\.\d{1,2}|100000(?:\.0{1,2})?)$/u;
const targetValues = new Set<CreditPackageTarget>(["personal", "team"]);
const statusValues = new Set<CreditPackageStatus>(["draft", "published", "archived"]);
const packageFields: (keyof CreditPackage)[] = ["name", "description", "targetType", "amountYuan", "status", "recommended", "sortOrder"];

export function packageAmount(value: string): string | null {
  const normalized = value.trim();
  if (!amountPattern.test(normalized)) return null;
  const number = Number(normalized);
  if (!Number.isFinite(number) || number < 1 || number > 100000) return null;
  return number.toFixed(2);
}

function creditForAmount(amount: string): string {
  return packageAmount(amount) ?? "0.00";
}

export function normalizePackage(value: CreditPackage): CreditPackage {
  const amountYuan = packageAmount(value.amountYuan) ?? "0.00";
  return {
    sku: value.sku.trim(),
    name: value.name.trim(),
    description: value.description.trim(),
    targetType: targetValues.has(value.targetType) ? value.targetType : "personal",
    amountYuan,
    creditAmount: creditForAmount(amountYuan),
    status: statusValues.has(value.status) ? value.status : "draft",
    recommended: Boolean(value.recommended),
    sortOrder: Number.isInteger(value.sortOrder) && value.sortOrder >= 0 ? value.sortOrder : 0,
  };
}

export function buildPackageUpdate(packages: CreditPackage[], expectedRevision: string | null) {
  return {
    expectedRevision,
    packages: packages.map(normalizePackage),
  };
}

export function validatePackages(packages: CreditPackage[]): string | null {
  const seen = new Set<string>();
  const recommended: Partial<Record<CreditPackageTarget, number>> = {};
  for (const item of packages) {
    if (!/^[a-z0-9][a-z0-9_-]{1,63}$/u.test(item.sku)) return `SKU「${item.sku}」需为 2–64 位小写字母、数字、连字符或下划线。`;
    if (seen.has(item.sku)) return `SKU「${item.sku}」重复。`;
    seen.add(item.sku);
    if (!item.name.trim() || [...item.name].length > 24) return `套餐「${item.sku}」名称需为 1–24 个字符。`;
    if ([...item.description].length > 48) return `套餐「${item.sku}」说明不能超过 48 个字符。`;
    if (!targetValues.has(item.targetType)) return `套餐「${item.sku}」的充值目标无效。`;
    if (!statusValues.has(item.status)) return `套餐「${item.sku}」的状态无效。`;
    if (!packageAmount(item.amountYuan)) return `套餐「${item.sku}」金额需为 1–100000 元，最多两位小数。`;
    if (!Number.isInteger(item.sortOrder) || item.sortOrder < 0 || item.sortOrder > 9999) return `套餐「${item.sku}」排序需为 0–9999 的整数。`;
    if (item.recommended) {
      recommended[item.targetType] = (recommended[item.targetType] ?? 0) + 1;
      if (recommended[item.targetType]! > 1) return `每种充值目标最多设置一个推荐套餐。`;
    }
  }
  return null;
}

export function publishedPackages(packages: CreditPackage[]): CreditPackage[] {
  return packages
    .filter((item) => item.status === "published")
    .sort((a, b) => a.targetType.localeCompare(b.targetType) || a.sortOrder - b.sortOrder || a.sku.localeCompare(b.sku));
}

export function recommendedByTarget(packages: CreditPackage[]): Partial<Record<CreditPackageTarget, string>> {
  const result: Partial<Record<CreditPackageTarget, string>> = {};
  for (const item of publishedPackages(packages)) {
    if (item.recommended && !result[item.targetType]) result[item.targetType] = item.sku;
  }
  return result;
}

/** 以最近一次服务端快照为基准合并本地草稿，避免 CAS 冲突时丢失输入。 */
export function mergePackageDraft(
  base: CreditPackage[],
  local: CreditPackage[],
  remote: CreditPackage[],
): { packages: CreditPackage[]; conflicts: { sku: string; field: keyof CreditPackage }[] } {
  const baseBySku = new Map(base.map((item) => [item.sku, item]));
  const localBySku = new Map(local.map((item) => [item.sku, item]));
  const remoteBySku = new Map(remote.map((item) => [item.sku, item]));
  const skus = [...new Set([...baseBySku.keys(), ...localBySku.keys(), ...remoteBySku.keys()])];
  const conflicts: { sku: string; field: keyof CreditPackage }[] = [];
  const merged = skus.flatMap((sku) => {
    const baseItem = baseBySku.get(sku);
    const localItem = localBySku.get(sku);
    const remoteItem = remoteBySku.get(sku);
    if (!localItem && remoteItem) return [remoteItem];
    if (!remoteItem && localItem) return [localItem];
    if (!localItem || !remoteItem) return [];
    if (!baseItem) {
      if (JSON.stringify(localItem) !== JSON.stringify(remoteItem)) conflicts.push({ sku, field: "name" });
      return [localItem];
    }
    const next = { ...remoteItem };
    for (const field of packageFields) {
      const localChanged = localItem[field] !== baseItem[field];
      const remoteChanged = remoteItem[field] !== baseItem[field];
      if (localChanged && remoteChanged && localItem[field] !== remoteItem[field]) conflicts.push({ sku, field });
      if (localChanged) next[field] = localItem[field] as never;
    }
    return [next];
  });
  return { packages: merged.map(normalizePackage), conflicts };
}

export function resolvePackageConflict(
  packages: CreditPackage[],
  remote: CreditPackage[],
  conflict: { sku: string; field: keyof CreditPackage },
  choice: "local" | "remote",
): CreditPackage[] {
  if (choice === "local") return packages;
  const localItem = packages.find((item) => item.sku === conflict.sku);
  const remoteItem = remote.find((item) => item.sku === conflict.sku);
  if (!remoteItem) return packages.filter((item) => item.sku !== conflict.sku);
  if (!localItem) return [...packages, remoteItem].map(normalizePackage);
  const next = packages.map((item) => item.sku === conflict.sku
    ? normalizePackage({ ...item, [conflict.field]: remoteItem[conflict.field] } as CreditPackage)
    : item);
  return next;
}
