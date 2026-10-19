import assert from "node:assert/strict";
import test from "node:test";
import {
  TURNSTILE_LOGIN_ACTION,
  normalizeTurnstileSiteKey,
  turnstileRequired,
} from "../src/turnstile.ts";

test("Turnstile is disabled unless the public config exposes a site key", () => {
  assert.equal(normalizeTurnstileSiteKey(undefined), "");
  assert.equal(normalizeTurnstileSiteKey(null), "");
  assert.equal(normalizeTurnstileSiteKey(123), "");
  assert.equal(normalizeTurnstileSiteKey("  1x00000000000000000000AA  "), "1x00000000000000000000AA");
  assert.equal(turnstileRequired(""), false);
  assert.equal(turnstileRequired("   "), false);
  assert.equal(turnstileRequired("1x00000000000000000000AA"), true);
  assert.equal(TURNSTILE_LOGIN_ACTION, "login");
});
