export interface ApiError extends Error {
  code?: number;
}

/** /console/api 客户端：携带会话 cookie，统一解包 { code, data, msg } 信封。 */
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`/console/api${path}`, {
    ...init,
    credentials: "include",
    headers: {
      "content-type": "application/json",
      ...(init.headers ?? {}),
    },
  });
  const body = (await response.json().catch(() => null)) as
    | { code?: number; data?: T; msg?: string }
    | null;
  if (!response.ok || (body?.code !== undefined && body.code !== 0)) {
    const error = new Error(body?.msg ?? `请求失败 (${response.status})`) as ApiError;
    error.code = body?.code ?? response.status;
    throw error;
  }
  return (body?.data ?? undefined) as T;
}

export function navigate(path: string): void {
  window.location.hash = path.startsWith("#") ? path : `#${path}`;
}
