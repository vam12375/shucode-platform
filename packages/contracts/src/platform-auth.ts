import { z } from "zod";

/**
 * 平台设备登录（桌面/Web 客户端 ↔ 平台控制面）的线上契约。
 *
 * 流程对齐 Shucode 现有 OAuth polling 形态：
 * init → 浏览器授权页 → 客户端轮询领取凭据；
 * 浏览器回跳 zcode://platform/callback 只负责唤醒窗口，凭据永不出现在 URL 中。
 */

export const PLATFORM_DEVICE_TOKEN_PREFIX = "mdp_";

export const platformDeviceTokenSchema = z
  .string()
  .regex(/^mdp_[A-Za-z0-9_-]{22,}$/, "invalid platform device token");

export const platformAuthFlowIdSchema = z.string().trim().min(16).max(64);

export const platformAuthInitRequestSchema = z
  .object({
    device_name: z.string().trim().max(120).optional(),
    platform: z.string().trim().max(60).optional(),
  })
  .strict();

export type PlatformAuthInitRequest = z.infer<typeof platformAuthInitRequestSchema>;

export const platformAuthInitResponseSchema = z.object({
  code: z.literal(0),
  data: z.object({
    flow_id: z.string().min(1),
    authorize_url: z.string().url(),
    /** Unix 秒。 */
    expires_at: z.number().int(),
    poll_interval_sec: z.number().positive(),
  }),
});

export type PlatformAuthInitResponse = z.infer<typeof platformAuthInitResponseSchema>;

export const platformAuthPollStatusSchema = z.enum([
  "pending",
  "authorized",
  "consumed",
  "expired",
  "denied",
]);

export type PlatformAuthPollStatus = z.infer<typeof platformAuthPollStatusSchema>;

export const platformWireUserSchema = z.object({
  id: z.string().min(1),
  email: z.string().min(1),
  display_name: z.string().min(1),
});

export const platformWireTeamSchema = z.object({
  id: z.string().min(1),
  name: z.string().min(1),
  role: z.enum(["owner", "member"]),
});

export const platformWireCredentialSchema = z.object({
  token: platformDeviceTokenSchema,
  token_id: z.string().min(1),
  token_hint: z.string().min(1),
});

export type PlatformWireUser = z.infer<typeof platformWireUserSchema>;
export type PlatformWireTeam = z.infer<typeof platformWireTeamSchema>;
export type PlatformWireCredential = z.infer<typeof platformWireCredentialSchema>;

export const platformAuthPollDataSchema = z.discriminatedUnion("status", [
  z.object({ status: z.literal("pending") }),
  z.object({
    status: z.literal("authorized"),
    credentials: platformWireCredentialSchema,
    user: platformWireUserSchema,
    teams: z.array(platformWireTeamSchema),
  }),
  z.object({ status: z.literal("consumed") }),
  z.object({ status: z.literal("expired") }),
  z.object({ status: z.literal("denied") }),
]);

export type PlatformAuthPollData = z.infer<typeof platformAuthPollDataSchema>;

export const platformAuthPollResponseSchema = z.object({
  code: z.literal(0),
  data: platformAuthPollDataSchema,
});

export type PlatformAuthPollResponse = z.infer<typeof platformAuthPollResponseSchema>;

export const PLATFORM_DEEP_LINK_CALLBACK_PREFIX = "zcode://platform/callback";

export function buildPlatformDeepLinkCallbackUrl(flowId: string): string {
  return `${PLATFORM_DEEP_LINK_CALLBACK_PREFIX}?flow_id=${encodeURIComponent(flowId)}`;
}

/** 用户级配置下发路由（Bearer 平台设备 token）。 */
export const USER_CONFIG_API_PATH = "/api/v1/client/user-config";
