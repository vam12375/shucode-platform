import assert from "node:assert/strict";
import test from "node:test";
import {
  defaultReconciliationOutcome,
  isReconciliationOutcomeForStage,
  pendingMediaChargeMicro,
  reconciliationOptionsForStage,
  reconciliationValueLabel,
  reconciliationValuePlaceholder,
} from "../src/media-reconciliation.ts";

test("image stage defaults to image recovery and never offers a video task ID", () => {
  const options = reconciliationOptionsForStage("image");
  assert.equal(defaultReconciliationOutcome("image"), "image_result");
  assert.deepEqual(options.map(({ value }) => value), ["image_result", "not_accepted"]);
  assert.equal(isReconciliationOutcomeForStage("image", "image_result"), true);
  assert.equal(isReconciliationOutcomeForStage("image", "video_task"), false);
});

test("video stage defaults to video recovery and never offers an image result URL", () => {
  const options = reconciliationOptionsForStage("video");
  assert.equal(defaultReconciliationOutcome("video"), "video_task");
  assert.deepEqual(options.map(({ value }) => value), ["video_task", "not_accepted"]);
  assert.equal(isReconciliationOutcomeForStage("video", "video_task"), true);
  assert.equal(isReconciliationOutcomeForStage("video", "image_result"), false);
});

test("unknown stage has no default or recovery options and cannot close a request identity", () => {
  for (const stage of [undefined, "", "other"]) {
    assert.equal(defaultReconciliationOutcome(stage), "");
    assert.deepEqual(reconciliationOptionsForStage(stage), []);
    assert.equal(isReconciliationOutcomeForStage(stage, "not_accepted"), false);
  }
});

test("recovery fields are labeled for the selected upstream result", () => {
  assert.equal(reconciliationValueLabel("image_result"), "图片结果 URL");
  assert.equal(reconciliationValuePlaceholder("image_result"), "可直接下载的图片结果地址");
  assert.equal(reconciliationValueLabel("video_task"), "视频 Task ID");
  assert.equal(reconciliationValuePlaceholder("video_task"), "供应商返回的视频任务 ID");
});

test("media reconciliation restores a locked stage price without treating it as a balance hold", () => {
  assert.equal(pendingMediaChargeMicro({ pendingChargeMicro: 125000, reservedMicro: 999999 }), 125000);
  assert.equal(pendingMediaChargeMicro({ reservedMicro: 75000 }), 75000);
  assert.equal(pendingMediaChargeMicro({ pendingChargeMicro: 0, reservedMicro: 75000 }), 75000);
  assert.equal(pendingMediaChargeMicro({}), 0);
  assert.equal(reconciliationOptionsForStage("image").find(({ value }) => value === "not_accepted").label, "确认未受理，零扣关闭");
});
