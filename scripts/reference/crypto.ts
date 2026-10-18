import {
  createCipheriv,
  createDecipheriv,
  createHash,
  randomBytes,
  randomUUID,
} from "node:crypto";

export function randomId(): string {
  return randomUUID();
}

/** 访问凭据（设备 token / 会话 token）一律只存 sha256 哈希。 */
export function hashToken(token: string): string {
  return createHash("sha256").update(token, "utf8").digest("hex");
}

export function generateDeviceToken(): string {
  return `mdp_${randomBytes(24).toString("base64url")}`;
}

export function generateSessionToken(): string {
  return randomBytes(32).toString("base64url");
}

export function generatePollToken(): string {
  return randomBytes(32).toString("hex");
}

export function deviceTokenHint(token: string): string {
  return token.slice(-4);
}

export interface SecretBox {
  encrypt(plaintext: string): string;
  decrypt(payload: string): string;
}

const SECRET_BOX_VERSION = "v1";

/**
 * AES-256-GCM：设备 token 必须以可恢复形式保存（user-config 会重新下发给同设备），
 * 但明文不能落库。密钥来自 PLATFORM_SECRET_KEY（32 字节 base64）。
 */
export function createSecretBox(secretKeyBase64: string): SecretBox {
  const key = Buffer.from(secretKeyBase64.trim(), "base64");
  if (key.length !== 32) {
    throw new Error("PLATFORM_SECRET_KEY 必须是 32 字节 base64");
  }
  return {
    encrypt(plaintext: string): string {
      const iv = randomBytes(12);
      const cipher = createCipheriv("aes-256-gcm", key, iv);
      const ciphertext = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
      const tag = cipher.getAuthTag();
      return [
        SECRET_BOX_VERSION,
        iv.toString("base64url"),
        ciphertext.toString("base64url"),
        tag.toString("base64url"),
      ].join(".");
    },
    decrypt(payload: string): string {
      const [version, ivPart, cipherPart, tagPart] = payload.split(".");
      if (version !== SECRET_BOX_VERSION || !ivPart || !cipherPart || !tagPart) {
        throw new Error("无法识别的凭据密文");
      }
      const decipher = createDecipheriv(
        "aes-256-gcm",
        key,
        Buffer.from(ivPart, "base64url"),
      );
      decipher.setAuthTag(Buffer.from(tagPart, "base64url"));
      return Buffer.concat([
        decipher.update(Buffer.from(cipherPart, "base64url")),
        decipher.final(),
      ]).toString("utf8");
    },
  };
}
