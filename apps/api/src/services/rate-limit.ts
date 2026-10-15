/** 极简内存限流：仅用于登录/表单类端点的暴力破解防护，单实例语义。 */
export function createRateLimiter(options: {
  windowMs: number;
  max: number;
}): (key: string, now?: number) => boolean {
  const buckets = new Map<string, number[]>();
  return (key: string, now = Date.now()): boolean => {
    const cutoff = now - options.windowMs;
    const entries = (buckets.get(key) ?? []).filter((timestamp) => timestamp > cutoff);
    if (entries.length >= options.max) {
      buckets.set(key, entries);
      return false;
    }
    entries.push(now);
    buckets.set(key, entries);
    return true;
  };
}
