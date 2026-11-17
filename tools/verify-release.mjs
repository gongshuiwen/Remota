import childProcess from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  cleanupDirectories,
  requireLinuxAMD64,
  unpackRelease,
} from "./release.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));

export function verifyRelease(directory, sourceRoot = root) {
  requireLinuxAMD64();
  if (!process.env.REMOTA_HTTPS_CHROMIUM)
    throw new Error("Set REMOTA_HTTPS_CHROMIUM to a Linux Chromium executable");
  const base = resolve(tmpdir());
  const temporary = mkdtempSync(join(base, "remota-release-check-"));
  let failure;
  try {
    const binaries = unpackRelease(resolve(directory), temporary, sourceRoot);
    const env = {
      ...process.env,
      GOOS: "linux",
      GOARCH: "amd64",
      REMOTA_BINARY_DIR: binaries,
    };
    function run(command, args, cwd) {
      const result = childProcess.spawnSync(command, args, {
        cwd,
        env,
        stdio: "inherit",
        timeout: 360_000,
      });
      if (result.error) throw result.error;
      if (result.status !== 0)
        throw new Error(`Release acceptance failed: ${command}`);
    }
    run(
      "go",
      [
        "test",
        "-mod=readonly",
        "-count=1",
        "-run",
        "^TestBinaryStartup$",
        "-timeout=45s",
        "./tests/integration",
      ],
      sourceRoot,
    );
    run("npm", ["run", "test:https"], join(sourceRoot, "web"));
    console.log(
      "PASS extracted Linux amd64 candidate: startup and HTTPS acceptance",
    );
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    cleanupDirectories(failure, [[temporary, base]]);
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    if (process.argv.length !== 3)
      throw new Error(
        "Usage: node tools/verify-release.mjs <candidate-directory>",
      );
    verifyRelease(process.argv[2]);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
