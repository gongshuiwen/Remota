import childProcess from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { build } from "./build.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const files = ["remota-server", "remota-agent", "RUNNING.md", "release.json"];
const digest = (file) =>
  createHash("sha256").update(readFileSync(file)).digest("hex");
const run = (command, args, cwd) =>
  childProcess.execFileSync(command, args, {
    cwd,
    encoding: "utf8",
    timeout: 120_000,
  });

function releaseName(version) {
  if (version.length > 40 || !/^\d+\.\d+\.\d+-rc\.[1-9]\d*$/.test(version))
    throw new Error("Expected a candidate version such as 0.1.0-rc.1");
  return `remota-${version}-linux-amd64`;
}

function candidateDirectory(version, sourceRoot) {
  const output = resolve(sourceRoot, "dist/releases", releaseName(version));
  if (existsSync(output))
    throw new Error("Candidate already exists; use a new version");
  return output;
}

// Each directory is owned by this operation and must remain under its recorded parent.
export function cleanupDirectories(failure, directories) {
  const errors = [];
  for (const [path, parent] of directories) {
    if (!path) continue;
    try {
      if (dirname(path) !== parent)
        throw new Error("Unexpected release cleanup path");
      rmSync(path, { recursive: true, force: true });
    } catch (error) {
      errors.push(error);
    }
  }
  if (errors.length) {
    throw new AggregateError(
      failure ? [failure, ...errors] : errors,
      [
        failure?.message,
        ...errors.map((error) => `Cleanup failed: ${error.message}`),
      ]
        .filter(Boolean)
        .join("\n"),
      { cause: failure ?? errors[0] },
    );
  }
}

export function requireLinuxAMD64() {
  if (process.platform !== "linux" || process.arch !== "x64")
    throw new Error(
      "Release packaging and acceptance require native Linux amd64 (or WSL)",
    );
}

function checkBinary(file) {
  const stat = lstatSync(file);
  if (!stat.isFile() || (stat.mode & 0o100) === 0)
    throw new Error("Release binary must be a regular executable file");
  const data = readFileSync(file);
  if (
    data.length < 64 ||
    data.subarray(0, 7).toString("hex") !== "7f454c46020101" ||
    data.readUInt16LE(18) !== 62
  )
    throw new Error("Release binary must be Linux amd64 ELF");
}

// Package only this fixed file list. Existing candidates are never replaced.
export function packageRelease(version, sourceRoot = root) {
  requireLinuxAMD64();
  const output = candidateDirectory(version, sourceRoot);
  const name = basename(output);
  const base = dirname(output);
  const binaries = join(sourceRoot, "dist/linux-amd64");
  for (const file of files.slice(0, 2)) checkBinary(join(binaries, file));
  const revision = run("git", ["rev-parse", "HEAD"], sourceRoot).trim();
  const dirty = run("git", ["status", "--porcelain"], sourceRoot).trim() !== "";
  mkdirSync(base, { recursive: true });
  const pending = mkdtempSync(join(base, ".candidate-"));
  const temporaryBase = resolve(tmpdir());
  let staging, failure;
  try {
    // WSL checkouts may live on NTFS, where chmod does not preserve POSIX modes.
    staging = mkdtempSync(join(temporaryBase, "remota-release-payload-"));
    const payload = join(staging, name);
    mkdirSync(payload, { mode: 0o755 });
    chmodSync(payload, 0o755);
    for (const file of files.slice(0, 2)) {
      copyFileSync(join(binaries, file), join(payload, file));
      chmodSync(join(payload, file), 0o755);
    }
    copyFileSync(
      join(sourceRoot, "docs/operations.md"),
      join(payload, "RUNNING.md"),
    );
    writeFileSync(
      join(payload, "release.json"),
      JSON.stringify(
        { version, revision, dirty, target: "linux-amd64" },
        null,
        2,
      ) + "\n",
    );
    for (const file of files.slice(2)) chmodSync(join(payload, file), 0o644);
    const archive = `${name}.tar.gz`;
    run("tar", [
      "--create",
      "--gzip",
      "--file",
      join(pending, archive),
      "--format=ustar",
      "--sort=name",
      "--mtime=@0",
      "--owner=0",
      "--group=0",
      "--numeric-owner",
      "-C",
      staging,
      name,
    ]);
    writeFileSync(
      join(pending, "SHA256SUMS"),
      `${digest(join(pending, archive))}  ${archive}\n`,
    );
    rmSync(staging, { recursive: true, force: true });
    staging = undefined;
    renameSync(pending, output);
    return output;
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    cleanupDirectories(failure, [
      [staging, temporaryBase],
      [pending, base],
    ]);
  }
}

// The output must be a new, owned temporary directory. Validate before extraction.
export function unpackRelease(directory, output, sourceRoot = root) {
  requireLinuxAMD64();
  const checksum = readFileSync(join(directory, "SHA256SUMS"), "utf8");
  const match =
    /^([a-f0-9]{64})  (remota-(\d+\.\d+\.\d+-rc\.[1-9]\d*)-linux-amd64)\.tar\.gz\n$/.exec(
      checksum,
    );
  if (!match || releaseName(match[3]) !== match[2])
    throw new Error("Invalid candidate checksum file");
  const [, hash, name, version] = match;
  const archive = join(directory, `${name}.tar.gz`);
  if (digest(archive) !== hash) throw new Error("Candidate checksum mismatch");
  const entries = run("tar", ["--list", "--gzip", "--file", archive])
    .replace(/\n$/, "")
    .split("\n");
  const modes = new Map([
    [`${name}/`, "drwxr-xr-x"],
    ...files.map((file, index) => [
      `${name}/${file}`,
      index < 2 ? "-rwxr-xr-x" : "-rw-r--r--",
    ]),
  ]);
  if (
    JSON.stringify([...entries].sort()) !==
    JSON.stringify([...modes.keys()].sort())
  )
    throw new Error("Unexpected candidate file list");
  const types = run("tar", ["--list", "--verbose", "--gzip", "--file", archive])
    .replace(/\n$/, "")
    .split("\n");
  if (
    types.length !== entries.length ||
    types.some((line, index) => line.slice(0, 10) !== modes.get(entries[index]))
  )
    throw new Error("Unexpected candidate file type or permissions");
  run("tar", [
    "--extract",
    "--gzip",
    "--file",
    archive,
    "--directory",
    output,
    "--no-same-owner",
    "--same-permissions",
  ]);
  const binaries = join(output, name);
  for (const file of files.slice(0, 2)) checkBinary(join(binaries, file));
  const info = JSON.parse(readFileSync(join(binaries, "release.json"), "utf8"));
  if (
    info.version !== version ||
    info.target !== "linux-amd64" ||
    typeof info.dirty !== "boolean" ||
    info.revision !== run("git", ["rev-parse", "HEAD"], sourceRoot).trim()
  )
    throw new Error(
      "Candidate metadata does not match the acceptance checkout",
    );
  if (process.env.CI === "true" && info.dirty)
    throw new Error("CI requires a clean candidate source");
  return binaries;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    requireLinuxAMD64();
    if (process.argv.length !== 3)
      throw new Error("Usage: node tools/release.mjs <version>");
    candidateDirectory(process.argv[2], root);
    process.env.GOOS = "linux";
    process.env.GOARCH = "amd64";
    build();
    console.log(`Packaged candidate: ${packageRelease(process.argv[2])}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = error.exitCode ?? 1;
  }
}
