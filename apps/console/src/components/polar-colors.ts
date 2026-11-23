export const POLAR_SERIES_COLORS = ["#3D93D6", "#D9703F", "#26A87D", "#B38D1F", "#A06AE0", "#DB5C80"] as const;

export function entityColor(key: string): string {
  const name = key.toLowerCase();
  const family = /gpt|openai|(?:^|[:/])o[134](?:-|$)/u.test(name) ? 0
    : /claude|anthropic/u.test(name) ? 1
      : /gemini/u.test(name) ? 2
        : /deepseek/u.test(name) ? 3
          : /qwen/u.test(name) ? 4
            : /kimi|moonshot/u.test(name) ? 5 : null;
  if (family !== null) return POLAR_SERIES_COLORS[family];
  let hash = 2166136261;
  for (const char of key) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619);
  return POLAR_SERIES_COLORS[(hash >>> 0) % POLAR_SERIES_COLORS.length];
}
