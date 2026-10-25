import { scopedPath } from "./permissions.js";

let selectedTeamId = "";
export function setApiTeamScope(teamId: string): void {
  selectedTeamId = teamId;
}
export function apiUrl(path: string): string {
  return `/console/api${scopedPath(path, selectedTeamId)}`;
}

export interface ApiError extends Error {
  code?: number;
  retryAfter?: number;
}

/** /console/api 客户端：携带会话 cookie，统一解包 { code, data, msg } 信封。 */
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const initHeaders = init.headers as Record<string, string> | undefined;
  const response = await fetch(apiUrl(path), {
    ...init,
    credentials: "include",
    headers: { "content-type": "application/json", ...initHeaders },
  });
  const body = (await response.json().catch(() => null)) as {
    code?: number;
    data?: T;
    msg?: string;
  } | null;
  if (!response.ok || (body?.code !== undefined && body.code !== 0)) {
    const error = new Error(
      body?.msg ?? `请求失败 (${response.status})`,
    ) as ApiError;
    error.code = body?.code ?? response.status;
    const retryAfter = Number.parseInt(
      response.headers.get("retry-after") ?? "",
      10,
    );
    if (Number.isFinite(retryAfter) && retryAfter > 0) {
      error.retryAfter = retryAfter;
    }
    throw error;
  }
  return (body?.data ?? undefined) as T;
}

export function navigate(path: string): void {
  window.location.hash = path.startsWith("#") ? path : `#${path}`;
}
