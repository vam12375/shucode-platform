-- 人民币成本价：模型增加空闲时段倍率；DeepSeek 官方模型按 CNY 高峰价入库。
-- 说明：platform_models 的价格字段即网关计费单价（1 credit = ¥1 的展示口径）；
-- 空闲价 = 高峰价 × off_peak_multiplier，由网关按北京时间窗口选择。

ALTER TABLE platform_models
  ADD COLUMN off_peak_multiplier numeric(18, 6) NOT NULL DEFAULT 1.0;

-- DeepSeek 官方人民币高峰价（缓存命中 = cache_read；缓存未命中 = input；输出 = output）。
-- 官方模型：deepseek-flash / deepseek-v4-pro；deepseek-v4.1-flash 作为历史别名同步价格。
UPDATE platform_models SET
  display_name = 'DeepSeek V4.1 Flash',
  input_price_per_mtok = 2.000000,
  output_price_per_mtok = 8.000000,
  cache_read_price_per_mtok = 0.040000,
  cache_write_price_per_mtok = 0.000000,
  off_peak_multiplier = 0.5000,
  updated_at = now()
WHERE model_id = 'deepseek-flash';

UPDATE platform_models SET
  display_name = 'DeepSeek V4 Pro',
  input_price_per_mtok = 9.000000,
  output_price_per_mtok = 27.000000,
  cache_read_price_per_mtok = 0.300000,
  cache_write_price_per_mtok = 0.000000,
  off_peak_multiplier = 0.5000,
  updated_at = now()
WHERE model_id = 'deepseek-v4-pro';

UPDATE platform_models SET
  input_price_per_mtok = 2.000000,
  output_price_per_mtok = 8.000000,
  cache_read_price_per_mtok = 0.040000,
  cache_write_price_per_mtok = 0.000000,
  off_peak_multiplier = 0.5000,
  updated_at = now()
WHERE model_id = 'deepseek-v4.1-flash';

-- 目录里没有 deepseek-v4-pro 时补建，沿用 deepseek-flash 的供应商。
INSERT INTO platform_models (
  model_id, display_name, upstream_model,
  input_price_per_mtok, output_price_per_mtok,
  cache_read_price_per_mtok, cache_write_price_per_mtok,
  off_peak_multiplier, enabled, sort_order, upstream_provider_id
)
SELECT
  'deepseek-v4-pro', 'DeepSeek V4 Pro', 'deepseek-v4-pro',
  9.000000, 27.000000, 0.300000, 0.000000,
  0.5000, true, 0,
  (SELECT upstream_provider_id FROM platform_models WHERE model_id = 'deepseek-flash')
WHERE NOT EXISTS (SELECT 1 FROM platform_models WHERE model_id = 'deepseek-v4-pro')
  AND EXISTS (
    SELECT 1 FROM platform_models
    WHERE model_id = 'deepseek-flash' AND upstream_provider_id IS NOT NULL
  );

-- 成本价库（KV）同步：DeepSeek 条目改为 CNY 高峰价 + 0.5 空闲倍率。
DO $cny_pricing$
DECLARE
  doc jsonb;
  entries jsonb;
  flash jsonb := '{
    "modelId": "deepseek-flash",
    "displayName": "DeepSeek V4.1 Flash",
    "inputCostPerMillion": "2.000000",
    "outputCostPerMillion": "8.000000",
    "cacheReadCostPerMillion": "0.040000",
    "cacheCreationCostPerMillion": "0.000000",
    "offPeakMultiplier": "0.500000",
    "currency": "CNY"
  }'::jsonb;
  pro jsonb := '{
    "modelId": "deepseek-v4-pro",
    "displayName": "DeepSeek V4 Pro",
    "inputCostPerMillion": "9.000000",
    "outputCostPerMillion": "27.000000",
    "cacheReadCostPerMillion": "0.300000",
    "cacheCreationCostPerMillion": "0.000000",
    "offPeakMultiplier": "0.500000",
    "currency": "CNY"
  }'::jsonb;
  legacy jsonb := '{
    "modelId": "deepseek-v4.1-flash",
    "displayName": "DeepSeek V4.1 Flash (legacy)",
    "inputCostPerMillion": "2.000000",
    "outputCostPerMillion": "8.000000",
    "cacheReadCostPerMillion": "0.040000",
    "cacheCreationCostPerMillion": "0.000000",
    "offPeakMultiplier": "0.500000",
    "currency": "CNY"
  }'::jsonb;
BEGIN
  SELECT value INTO doc FROM platform_config WHERE key = 'model_pricing_v1';
  IF doc IS NULL OR jsonb_typeof(doc->'entries') IS DISTINCT FROM 'array' THEN
    RETURN;
  END IF;

  entries := (
    SELECT COALESCE(jsonb_agg(
      CASE elem->>'modelId'
        WHEN 'deepseek-flash' THEN elem || flash
        WHEN 'deepseek-v4-pro' THEN elem || pro
        WHEN 'deepseek-v4.1-flash' THEN elem || legacy
        ELSE elem
      END
    ), '[]'::jsonb)
    FROM jsonb_array_elements(doc->'entries') AS elem
  );

  IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements(entries) e WHERE e->>'modelId' = 'deepseek-v4-pro') THEN
    entries := entries || jsonb_build_array(pro);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements(entries) e WHERE e->>'modelId' = 'deepseek-v4.1-flash') THEN
    entries := entries || jsonb_build_array(legacy);
  END IF;

  UPDATE platform_config
  SET value = jsonb_set(doc, '{entries}', entries), updated_at = now()
  WHERE key = 'model_pricing_v1';
END
$cny_pricing$;
