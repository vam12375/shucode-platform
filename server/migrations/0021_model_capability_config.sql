-- 客户端模型能力配置由平台统一管理：
-- recommended = 跟随官方内置规则；manual = 下发 model_config 覆盖 properties/optionSpecs。
ALTER TABLE platform_models
  ADD COLUMN config_mode text NOT NULL DEFAULT 'recommended'
    CHECK (config_mode IN ('recommended', 'manual')),
  ADD COLUMN model_config jsonb;
