import assert from "node:assert/strict";
import test from "node:test";
import { alipayURL, paymentAmount, rechargeTeams, restorePaymentAttempt, sameAttempt } from "../src/payments.ts";

test("充值金额以分规范化，普通充值不接受0.01测试金额", () => {
  for (const [value, expected] of [["1", "1.00"], [" 25.1 ", "25.10"], ["100000.00", "100000.00"], ["1.01", "1.01"]]) assert.equal(paymentAmount(value), expected);
  for (const value of ["", "0", "0.01", "0.99", "-1", "1e2", "01", "1.", "1.001", "100000.01", "Infinity"]) assert.equal(paymentAmount(value), null, value);
});

test("支付跳转仅接受官方HTTPS网关", () => {
  for (const host of ["openapi.alipay.com", "openapi-sandbox.dl.alipaydev.com"]) assert.equal(alipayURL(`https://${host}/gateway.do?sign=fixture`), `https://${host}/gateway.do?sign=fixture`);
  for (const value of ["javascript:alert(1)", "http://openapi.alipay.com/gateway.do", "https://openapi.alipay.com.evil.test/gateway.do", "https://evil.test/gateway.do", "https://user@openapi.alipay.com/gateway.do", "https://openapi.alipay.com:444/gateway.do", "https://openapi.alipay.com/other", "https://openapi.alipay.com/gateway.do#fragment", "/gateway.do"]) assert.throws(() => alipayURL(value), undefined, value);
});

test("团队充值选项只包含有效管理关系，普通成员不能充值团队", () => {
  const teams = [{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "c", name: "C", disabledAt: "disabled" }];
  const memberships = [{ team: teams[0], role: "member", memberDisabledAt: null }, { team: teams[1], role: "admin", memberDisabledAt: null }, { team: teams[2], role: "owner", memberDisabledAt: null }];
  assert.deepEqual(rechargeTeams(teams, memberships, false).map((team) => team.id), ["b"]);
  assert.deepEqual(rechargeTeams(teams, memberships, true).map((team) => team.id), ["a", "b"]);
  assert.deepEqual(rechargeTeams(teams, [{ ...memberships[1], memberDisabledAt: "disabled" }], false), []);
});

test("网络失败后的相同充值草稿复用requestId，金额或目标改变产生新意图", () => {
  const attempt = { requestId: "11111111-1111-4111-8111-111111111111", targetType: "personal", amountYuan: "1.00", mobile: false };
  assert.equal(sameAttempt(attempt, { targetType: "personal", amountYuan: "1.00", mobile: false }), true);
  for (const patch of [{ amountYuan: "2.00" }, { targetType: "team", teamId: "team-a" }, { testPayment: true }, { mobile: true }]) assert.equal(sameAttempt(attempt, { ...attempt, ...patch }), false);
  assert.deepEqual(restorePaymentAttempt(JSON.stringify(attempt)), attempt);
  assert.equal(restorePaymentAttempt("{broken"), null);
  assert.equal(restorePaymentAttempt(JSON.stringify({ ...attempt, amountYuan: "1e4" })), null);
  assert.equal(restorePaymentAttempt(JSON.stringify({ ...attempt, requestId: "invalid" })), null);
  assert.equal(restorePaymentAttempt(JSON.stringify({ ...attempt, amountYuan: "0.01", testPayment: true })).testPayment, true);
});
