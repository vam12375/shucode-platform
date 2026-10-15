import { hashToken, randomId, type DeviceTokenRecord, type UserRecord } from "@shucode-platform/db";
import { getCookie, setCookie, deleteCookie } from "hono/cookie";
import type { Context } from "hono";
import type { AppContext } from "../context.js";
import { isoNow } from "../context.js";

/** 控制台与设备登录页共用同一用户体系，因此也共用同一个会话 cookie。 */
export const SESSION_COOKIE_NAME = "mendao_session";
const SESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000;

export async function readSessionUser(
  context: AppContext,
  c: Context,
): Promise<UserRecord | null> {
  const raw = getCookie(c, SESSION_COOKIE_NAME);
  if (!raw) return null;
  const session = await context.store.findSession(hashToken(raw));
  if (!session) return null;
  if (Date.parse(session.expiresAt) <= context.now()) {
    await context.store.deleteSession(session.tokenHash);
    return null;
  }
  const user = await context.store.findUserById(session.userId);
  if (!user || user.disabledAt) return null;
  await context.store.touchSession(session.tokenHash, isoNow(context));
  return user;
}

export async function loginWithSession(
  context: AppContext,
  c: Context,
  userId: string,
): Promise<void> {
  const token = `${randomId()}.${Math.random().toString(36).slice(2)}`;
  const now = context.now();
  await context.store.createSession({
    tokenHash: hashToken(token),
    userId,
    createdAt: new Date(now).toISOString(),
    expiresAt: new Date(now + SESSION_TTL_MS).toISOString(),
    lastSeenAt: new Date(now).toISOString(),
  });
  setCookie(c, SESSION_COOKIE_NAME, token, {
    path: "/",
    httpOnly: true,
    sameSite: "Lax",
    secure: context.env.secureCookies,
    maxAge: Math.floor(SESSION_TTL_MS / 1000),
    ...(context.env.cookieDomain ? { domain: context.env.cookieDomain } : {}),
  });
}

export async function logoutSession(context: AppContext, c: Context): Promise<void> {
  const raw = getCookie(c, SESSION_COOKIE_NAME);
  if (raw) {
    await context.store.deleteSession(hashToken(raw));
  }
  deleteCookie(c, SESSION_COOKIE_NAME, {
    path: "/",
    ...(context.env.cookieDomain ? { domain: context.env.cookieDomain } : {}),
  });
}

export function readBearerToken(c: Context): string | null {
  const header = c.req.header("authorization")?.trim();
  if (header?.toLowerCase().startsWith("bearer ")) {
    const token = header.slice(7).trim();
    return token || null;
  }
  const apiKey = c.req.header("x-api-key")?.trim();
  return apiKey || null;
}

export interface AuthenticatedDevice {
  user: UserRecord;
  device: DeviceTokenRecord;
  plaintextToken: string | null;
}

export async function authenticateDevice(
  context: AppContext,
  c: Context,
): Promise<AuthenticatedDevice | null> {
  const token = readBearerToken(c);
  if (!token) return null;
  const device = await context.store.findDeviceTokenByHash(hashToken(token));
  if (!device || device.revokedAt) return null;
  const user = await context.store.findUserById(device.userId);
  if (!user || user.disabledAt) return null;
  await context.store.touchDeviceToken(device.id, isoNow(context));
  let plaintextToken: string | null = null;
  if (context.secretBox) {
    try {
      plaintextToken = context.secretBox.decrypt(device.tokenCiphertext);
    } catch (error) {
      context.logger.warn("设备 token 解密失败，user-config 将不携带凭据", { error });
    }
  }
  return { user, device, plaintextToken };
}
