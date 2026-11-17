import assert from "node:assert/strict";
import childProcess from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, test } from "node:test";
import { packageRelease, unpackRelease } from "./release.mjs";
import { verifyRelease } from "./verify-release.mjs";

const revision = "a".repeat(40);
const version = "0.1.0-rc.1";
const project = fileURLToPath(new URL("../", import.meta.url));

function fixture(t, archiveFailure) {
  const base = resolve(tmpdir());
  const root = mkdtempSync(join(base, "remota-release-test-"));
  t.after(() => {
    if (dirname(root) !== base)
      throw new Error("Unexpected fixture cleanup path");
    rmSync(root, { recursive: true, force: true });
  });
  const binaries = join(root, "dist/linux-amd64");
  mkdirSync(binaries, { recursive: true });
  const elf = Buffer.alloc(64);
  Buffer.from("7f454c46020101", "hex").copy(elf);
  elf.writeUInt16LE(62, 18);
  for (const file of ["remota-server", "remota-agent"])
    writeFileSync(join(binaries, file), elf, { mode: 0o755 });
  mkdirSync(join(root, "docs"));
  writeFileSync(join(root, "docs/operations.md"), "# Run the candidate\n");
  const exec = childProcess.execFileSync;
  t.mock.method(childProcess, "execFileSync", (command, args, options) => {
    if (command === "git")
      return args[0] === "rev-parse" ? revision + "\n" : "";
    if (command === "tar" && archiveFailure) throw archiveFailure;
    return exec(command, args, options);
  });
  const extracted = join(root, "unpacked");
  mkdirSync(extracted);
  return { root, binaries, elf, extracted };
}

function environment(t, key, value) {
  const previous = process.env[key];
  process.env[key] = value;
  t.after(() => {
    if (previous === undefined) delete process.env[key];
    else process.env[key] = previous;
  });
}

function removalFailure(t, prefix, error) {
  const base = resolve(tmpdir());
  const remove = fs.rmSync;
  const failed = new Set();
  const mock = t.mock.method(fs, "rmSync", (path, options) => {
    if (dirname(path) === base && basename(path).startsWith(prefix)) {
      failed.add(path);
      throw error;
    }
    return remove(path, options);
  });
  syncBuiltinESMExports();
  t.after(() => {
    mock.mock.restore();
    syncBuiltinESMExports();
    for (const path of failed) {
      if (dirname(path) !== base)
        throw new Error("Unexpected fault fixture path");
      remove(path, { recursive: true, force: true });
    }
  });
}

function reseal(candidate, payload) {
  const archive = `${basename(candidate)}.tar.gz`;
  childProcess.execFileSync("tar", [
    "--sort=name",
    "-czf",
    join(candidate, archive),
    "-C",
    dirname(payload),
    basename(payload),
  ]);
  const hash = createHash("sha256")
    .update(readFileSync(join(candidate, archive)))
    .digest("hex");
  writeFileSync(join(candidate, "SHA256SUMS"), `${hash}  ${archive}\n`);
}

describe(
  "Linux amd64 candidate packaging",
  { skip: process.platform !== "linux" || process.arch !== "x64" },
  () => {
    test("fixed payload round-trips with provenance and modes; existing candidate is preserved", (t) => {
      const f = fixture(t);
      writeFileSync(join(f.binaries, "not-for-release.log"), "fixture only");
      chmodSync(join(f.binaries, "remota-agent"), 0o777);
      const mask = process.umask(0o077);
      t.after(() => process.umask(mask));
      const candidate = packageRelease(version, f.root);
      const binaries = unpackRelease(candidate, f.extracted, f.root);
      assert.deepEqual(readdirSync(binaries).sort(), [
        "RUNNING.md",
        "release.json",
        "remota-agent",
        "remota-server",
      ]);
      assert.deepEqual(readFileSync(join(binaries, "remota-agent")), f.elf);
      assert.equal(
        statSync(join(binaries, "remota-agent")).mode & 0o777,
        0o755,
      );
      assert.equal(statSync(join(binaries, "RUNNING.md")).mode & 0o777, 0o644);
      assert.deepEqual(
        JSON.parse(readFileSync(join(binaries, "release.json"))),
        {
          version,
          revision,
          dirty: false,
          target: "linux-amd64",
        },
      );
      const before = readFileSync(join(candidate, "SHA256SUMS"));
      assert.throws(() => packageRelease(version, f.root), /already exists/);
      assert.deepEqual(readFileSync(join(candidate, "SHA256SUMS")), before);
    });

    for (const { name, mutate, error } of [
      {
        name: "missing binary",
        mutate: (f) => rmSync(join(f.binaries, "remota-agent")),
        error: /ENOENT/,
      },
      {
        name: "wrong architecture",
        mutate: (f) => {
          f.elf.writeUInt16LE(183, 18);
          writeFileSync(join(f.binaries, "remota-agent"), f.elf);
        },
        error: /Linux amd64 ELF/,
      },
      {
        name: "missing guide",
        mutate: (f) => rmSync(join(f.root, "docs/operations.md")),
        error: /ENOENT/,
      },
    ]) {
      test(`${name} cannot publish a partial candidate`, (t) => {
        const f = fixture(t);
        mutate(f);
        assert.throws(() => packageRelease(version, f.root), error);
        const base = join(f.root, "dist/releases");
        assert.deepEqual(existsSync(base) ? readdirSync(base) : [], []);
      });
    }

    test("invalid version and archive damage are rejected before extraction", (t) => {
      const f = fixture(t);
      assert.throws(
        () => packageRelease("../../outside", f.root),
        /candidate version/,
      );
      const candidate = packageRelease(version, f.root);
      writeFileSync(
        join(candidate, `${basename(candidate)}.tar.gz`),
        "damaged fixture",
      );
      assert.throws(
        () => unpackRelease(candidate, f.extracted, f.root),
        /checksum mismatch/,
      );
      assert.deepEqual(readdirSync(f.extracted), []);
    });

    for (const { name, mutate, error, beforeExtract } of [
      {
        name: "extra file",
        mutate: (p) => writeFileSync(join(p, "extra"), "fixture"),
        error: /file list/,
        beforeExtract: true,
      },
      {
        name: "filename whitespace",
        mutate: (p) =>
          renameSync(join(p, "remota-server"), join(p, "remota-server ")),
        error: /file list/,
        beforeExtract: true,
      },
      {
        name: "symlink",
        mutate: (p) => {
          rmSync(join(p, "RUNNING.md"));
          symlinkSync("remota-agent", join(p, "RUNNING.md"));
        },
        error: /file type or permissions/,
        beforeExtract: true,
      },
      ...[
        ["lost execute permission", "remota-agent", 0o644],
        ["writable program", "remota-agent", 0o777],
        ["executable guide", "RUNNING.md", 0o777],
        ["writable directory", "", 0o777],
      ].map(([name, file, mode]) => ({
        name,
        mutate: (p) => chmodSync(join(p, file), mode),
        error: /file type or permissions/,
        beforeExtract: true,
      })),
      {
        name: "wrong revision",
        mutate: (p) => {
          const path = join(p, "release.json");
          const info = JSON.parse(readFileSync(path));
          writeFileSync(
            path,
            JSON.stringify({ ...info, revision: "b".repeat(40) }),
          );
        },
        error: /metadata/,
      },
      {
        name: "dirty CI source",
        mutate: (p, t) => {
          const path = join(p, "release.json");
          const info = JSON.parse(readFileSync(path));
          writeFileSync(path, JSON.stringify({ ...info, dirty: true }));
          environment(t, "CI", "true");
        },
        error: /clean candidate source/,
      },
    ]) {
      test(`a valid checksum cannot hide ${name}`, (t) => {
        const f = fixture(t);
        const candidate = packageRelease(version, f.root);
        const payload = unpackRelease(candidate, f.extracted, f.root);
        mutate(payload, t);
        reseal(candidate, payload);
        const second = join(f.root, "second-unpack");
        mkdirSync(second);
        assert.throws(() => unpackRelease(candidate, second, f.root), error);
        if (beforeExtract) assert.deepEqual(readdirSync(second), []);
      });
    }

    for (const fails of [false, true]) {
      test(`acceptance uses extracted binaries and cleans after subprocess failure=${fails}`, (t) => {
        const f = fixture(t);
        const candidate = packageRelease(version, f.root);
        environment(t, "REMOTA_HTTPS_CHROMIUM", "/fixture-browser");
        const calls = [];
        t.mock.method(childProcess, "spawnSync", (command, _args, options) => {
          const path = options.env.REMOTA_BINARY_DIR;
          assert.notEqual(path, f.binaries);
          assert.equal(existsSync(join(path, "remota-agent")), true);
          calls.push({ command, path });
          return { status: fails ? 7 : 0 };
        });
        if (fails)
          assert.throws(
            () => verifyRelease(candidate, f.root),
            /acceptance failed: go/,
          );
        else verifyRelease(candidate, f.root);
        assert.deepEqual(
          calls.map((call) => call.command),
          fails ? ["go"] : ["go", "npm"],
        );
        assert.equal(existsSync(calls[0].path), false);
      });
    }

    for (const primary of [undefined, new Error("archive failed")]) {
      test(`packaging retains the primary error and cleans other directories; archive failure=${!!primary}`, (t) => {
        const f = fixture(t, primary);
        const secondary = new Error("payload cleanup blocked");
        removalFailure(t, "remota-release-payload-", secondary);
        assert.throws(
          () => packageRelease(version, f.root),
          (error) => {
            assert.ok(error instanceof AggregateError);
            assert.equal(error.cause, primary ?? secondary);
            assert.equal(error.errors.includes(secondary), true);
            if (primary) assert.match(error.message, /archive failed/);
            assert.match(error.message, /payload cleanup blocked/);
            return true;
          },
        );
        assert.deepEqual(readdirSync(join(f.root, "dist/releases")), []);
      });
    }

    test("acceptance retains the subprocess failure when directory cleanup also fails", (t) => {
      const f = fixture(t);
      const candidate = packageRelease(version, f.root);
      environment(t, "REMOTA_HTTPS_CHROMIUM", "/fixture-browser");
      const secondary = new Error("acceptance cleanup blocked");
      removalFailure(t, "remota-release-check-", secondary);
      t.mock.method(childProcess, "spawnSync", () => ({ status: 7 }));
      assert.throws(
        () => verifyRelease(candidate, f.root),
        (error) => {
          assert.ok(error instanceof AggregateError);
          assert.match(error.cause.message, /acceptance failed: go/);
          assert.equal(error.errors[1], secondary);
          assert.match(error.message, /acceptance cleanup blocked/);
          return true;
        },
      );
    });

    test("CLI rejects an existing candidate before invoking build tools", (t) => {
      const f = fixture(t);
      packageRelease(version, f.root);
      mkdirSync(join(f.root, "tools"));
      for (const file of ["release.mjs", "build.mjs"])
        copyFileSync(join(project, "tools", file), join(f.root, "tools", file));
      const result = childProcess.spawnSync(
        process.execPath,
        [join(f.root, "tools/release.mjs"), version],
        {
          env: { PATH: "" },
          encoding: "utf8",
          timeout: 5_000,
        },
      );
      assert.equal(result.status, 1);
      assert.match(result.stderr, /Candidate already exists/);
      assert.equal(result.stdout, "");
    });

    test("explicit artifact mode bypasses rebuild; empty paths fail instead of selecting defaults", () => {
      const pretest = join(project, "web/e2e/build-https.mjs");
      for (const [directory, status] of [
        ["/explicit-missing-artifacts", 0],
        ["", 1],
      ]) {
        const result = childProcess.spawnSync(process.execPath, [pretest], {
          env: { PATH: "", REMOTA_BINARY_DIR: directory },
          encoding: "utf8",
          timeout: 5_000,
        });
        assert.equal(result.status, status, result.stderr);
        assert.equal(result.stdout.includes("Built"), false);
      }
    });
  },
);
