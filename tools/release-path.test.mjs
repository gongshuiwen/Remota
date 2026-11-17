import assert from "node:assert/strict";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import prepareHTTPS, { startHTTPS } from "../web/e2e/https.ts";

test(
  "missing explicit binaries fail even when default products exist",
  {
    skip: process.platform !== "linux" || process.arch !== "x64",
    timeout: 150_000,
  },
  async (t) => {
    const root = fileURLToPath(new URL("../", import.meta.url));
    for (const name of ["remota-server", "remota-agent"])
      assert.ok(
        existsSync(join(root, "dist/linux-amd64", name)),
        "Build Linux amd64 products before the path regression",
      );
    const base = resolve(tmpdir());
    const temporary = mkdtempSync(join(base, "remota-path-test-"));
    const keys = [
      "REMOTA_BINARY_DIR",
      "REMOTA_HTTPS_CHROMIUM",
      "REMOTA_HTTPS_HELPER",
      "REMOTA_HTTPS_BINARIES",
    ];
    const saved = keys.map((key) => process.env[key]);
    t.after(() => {
      keys.forEach((key, index) => {
        if (saved[index] === undefined) delete process.env[key];
        else process.env[key] = saved[index];
      });
      if (dirname(temporary) !== base)
        throw new Error("Unexpected path regression cleanup directory");
      rmSync(temporary, { recursive: true, force: true });
    });
    const missing = join(temporary, "missing-binaries");
    process.env.REMOTA_BINARY_DIR = missing;
    process.env.REMOTA_HTTPS_CHROMIUM = "/not-needed-for-missing-binaries";
    const clean = await prepareHTTPS();
    try {
      await assert.rejects(
        startHTTPS().then(async (products) => {
          await products.stop();
        }),
        (error) => {
          assert.ok(
            error.message.includes(missing),
            "Controller did not report the explicit missing binary directory",
          );
          return true;
        },
      );
    } finally {
      await clean();
    }
  },
);
