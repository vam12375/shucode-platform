import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { build } from "vite";

test("production assets enforce public, session, platform and setup boundaries", async () => {
  const outDir = await mkdtemp(join(tmpdir(), "console-access-"));
  try {
    const root = fileURLToPath(new URL("..", import.meta.url));
    const output = await build({ root, configFile: join(root, "vite.config.ts"), logLevel: "silent", build: { outDir } });
    const policy = JSON.parse(await readFile(join(outDir, "asset-access.json"), "utf8"));
    const manifest = JSON.parse(await readFile(join(outDir, ".vite/manifest.json"), "utf8"));
    assert.equal(policy.version, 1);
    for (const [entry, level] of Object.entries({ "index.html": "public", "src/App.tsx": "session", "src/RootConsole.tsx": "platform", "src/pages/RootSetup.tsx": "setup", "src/pages/PaymentConfig.tsx": "platform" })) {
      assert.equal(policy.files[manifest[entry].file], level, entry);
    }
    for (const chunk of output.output.filter((item) => item.type === "chunk")) {
      assert.ok(policy.files[chunk.fileName], chunk.fileName);
      assert.doesNotMatch(chunk.code, /!~\{|sourceMappingURL|VITE_APP_SUBJECT|BOOTSTRAP_SECRET/);
      if (["public", "session"].includes(policy.files[chunk.fileName])) {
        assert.doesNotMatch(chunk.code, /toapis|toapis\.com|ROOT SETUP|role\s*[:=]+\s*["'`]root/i);
      }
      if (policy.files[chunk.fileName] === "public") {
        for (const name of ["RootSetupPage", "rootInitialized", "BOOTSTRAP_SECRET"]) {
          assert.ok(!chunk.code.includes(name) && !chunk.code.includes(Buffer.from(name).toString("base64")), `private role symbol shipped publicly: ${name}`);
        }
      }
    }
    assert.ok((await readdir(join(outDir, "assets"))).every((name) => !name.endsWith(".map")));
    const html = await readFile(join(outDir, "index.html"), "utf8");
    for (const [file, level] of Object.entries(policy.files)) {
      if (level !== "public") assert.ok(!html.includes(file), `private asset preloaded: ${file}`);
    }
  } finally { await rm(outDir, { recursive: true, force: true }); }
});
