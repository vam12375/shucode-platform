ALTER TABLE payment_orders
 ADD COLUMN purchase_kind text NOT NULL DEFAULT 'custom' CHECK (purchase_kind IN ('custom','plan','root_test')),
 ADD COLUMN package_snapshot jsonb;
UPDATE payment_orders SET purchase_kind='root_test' WHERE test_payment;
ALTER TABLE payment_orders ADD CONSTRAINT payment_orders_package_snapshot_check
 CHECK ((purchase_kind='plan') = (package_snapshot IS NOT NULL));

INSERT INTO platform_config(key,value,updated_at)
VALUES ('payments:packages:version:00000000-0000-4000-8000-000000003035',
 '{"revision":"00000000-0000-4000-8000-000000003035","previousRevision":"","publishedAt":null,"publishedBy":"system","packages":[{"sku":"personal-lite","name":"轻量包","description":"个人一次性 credit 套餐","targetType":"personal","amountFen":2900,"status":"draft","recommended":false,"sortOrder":10},{"sku":"personal-standard","name":"常用包","description":"个人一次性 credit 套餐","targetType":"personal","amountFen":9900,"status":"draft","recommended":true,"sortOrder":20},{"sku":"personal-creator","name":"创作包","description":"个人一次性 credit 套餐","targetType":"personal","amountFen":29900,"status":"draft","recommended":false,"sortOrder":30},{"sku":"team-collaboration","name":"协作包","description":"团队一次性 credit 套餐","targetType":"team","amountFen":99900,"status":"draft","recommended":true,"sortOrder":10},{"sku":"team-studio","name":"工作室包","description":"团队一次性 credit 套餐","targetType":"team","amountFen":299900,"status":"draft","recommended":false,"sortOrder":20}]}',now())
ON CONFLICT(key) DO NOTHING;
INSERT INTO platform_config(key,value,updated_at)
VALUES ('payments:packages:current','{"revision":"00000000-0000-4000-8000-000000003035"}',now())
ON CONFLICT(key) DO NOTHING;

ALTER TABLE payment_orders ADD CONSTRAINT payment_orders_plan_snapshot_values_check
 CHECK (package_snapshot IS NULL OR
  (package_snapshot->>'sku' IS NOT NULL AND package_snapshot->>'catalogRevision' IS NOT NULL AND
   package_snapshot->>'name' IS NOT NULL AND package_snapshot->>'targetType'=target_type AND
   (package_snapshot->>'amountFen')::bigint=amount_fen AND
   (package_snapshot->>'creditMicro')::bigint=credit_micro));
