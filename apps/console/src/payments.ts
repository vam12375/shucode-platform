import type { ConsoleTeam, Membership } from "./permissions.js";

export type PaymentTarget = "personal" | "team";
export interface PaymentAttempt { requestId: string; targetType: PaymentTarget; teamId?: string; amountYuan: string; testPayment?: boolean; mobile: boolean }
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/iu;

export function paymentAmount(value: string): string | null {
  const normalized = value.trim();
  if (!/^(?:0|[1-9]\d{0,5})(?:\.\d{1,2})?$/u.test(normalized)) return null;
  const [whole, fraction = ""] = normalized.split(".");
  const fen = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, "0"));
  return fen >= 100n && fen <= 10_000_000n ? `${whole}.${fraction.padEnd(2, "0")}` : null;
}

export function alipayURL(value: string): string {
  const url = new URL(value);
  if (url.protocol !== "https:" || !["openapi.alipay.com", "openapi-sandbox.dl.alipaydev.com"].includes(url.hostname) || url.pathname !== "/gateway.do" || url.username || url.password || url.port || url.hash) throw new Error("支付链接不是支付宝官方网关，请联系管理员核查");
  return url.href;
}

export function rechargeTeams(teams: ConsoleTeam[], memberships: Membership[], isRoot: boolean): ConsoleTeam[] {
  return teams.filter((team) => !team.disabledAt && (isRoot || memberships.some((item) => item.team.id === team.id && !item.memberDisabledAt && !item.team.disabledAt && ["owner", "admin"].includes(item.role))));
}

export function sameAttempt(a: PaymentAttempt, b: Omit<PaymentAttempt, "requestId">): boolean {
  return a.targetType === b.targetType && (a.teamId ?? "") === (b.teamId ?? "") && a.amountYuan === b.amountYuan && !!a.testPayment === !!b.testPayment && a.mobile === b.mobile;
}

export function restorePaymentAttempt(raw: string | null): PaymentAttempt | null {
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as PaymentAttempt;
    if (!value || !uuidPattern.test(value.requestId) || !["personal", "team"].includes(value.targetType) || typeof value.mobile !== "boolean" || (value.targetType === "team" && typeof value.teamId !== "string") || (value.testPayment !== undefined && typeof value.testPayment !== "boolean") || (value.amountYuan !== "0.01" && paymentAmount(value.amountYuan) !== value.amountYuan)) return null;
    return value;
  } catch { return null; }
}
