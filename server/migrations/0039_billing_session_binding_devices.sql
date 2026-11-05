CREATE TABLE billing_session_binding_devices (
 binding_id uuid NOT NULL REFERENCES billing_session_bindings(id) ON DELETE RESTRICT,
 device_token_id uuid NOT NULL REFERENCES device_tokens(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(binding_id, device_token_id)
);

INSERT INTO billing_session_binding_devices (binding_id, device_token_id, created_at)
SELECT id, device_token_id, created_at FROM billing_session_bindings;
