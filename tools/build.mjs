import childProcess from "node:child_process";
import { mkdirSync, mkdtempSync, renameSync, rmSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export function build(root = fileURLToPath(new URL("../", import.meta.url))) {
  const web = join(root, "web");
  const target = JSON.parse(
    childProcess.execFileSync("go", ["env", "-json", "GOOS", "GOARCH"], {
      cwd: root,
      encoding: "utf8",
      windowsHide: true,
    }),
  );
  if (
    !["linux", "windows", "darwin"].includes(target.GOOS) ||
    !["amd64", "arm64"].includes(target.GOARCH)
  ) {
    throw new Error(
      "Build target must be linux/windows/darwin and amd64/arm64",
    );
  }
  function run(command, args, cwd = root) {
    const result = childProcess.spawnSync(command, args, {
      cwd,
      stdio: "inherit",
      windowsHide: true,
      env: { ...process.env, CGO_ENABLED: "0" },
    });
    if (result.error) throw result.error;
    if (result.status !== 0)
      throw Object.assign(new Error(`${command} failed`), {
        exitCode: result.status ?? 1,
      });
  }
  for (const args of [["ci"], ["run", "build"]]) {
    if (process.platform === "win32")
      run("cmd.exe", ["/d", "/s", "/c", ["npm.cmd", ...args].join(" ")], web);
    else run("npm", args, web);
  }

  const base = resolve(root, "dist");
  const platform = `${target.GOOS}-${target.GOARCH}`;
  const output = join(base, platform);
  mkdirSync(base, { recursive: true });
  const pending = mkdtempSync(join(base, `.${platform}-`));
  if (dirname(output) !== base || dirname(pending) !== base)
    throw new Error("Unexpected build directory");
  try {
    for (const name of ["remota-server", "remota-agent"]) {
      const filename = name + (target.GOOS === "windows" ? ".exe" : "");
      run("go", [
        "build",
        "-mod=readonly",
        "-trimpath",
        "-o",
        join(pending, filename),
        `./cmd/${name}`,
      ]);
    }
    rmSync(output, { recursive: true, force: true });
    renameSync(pending, output);
  } finally {
    rmSync(pending, { recursive: true, force: true });
  }
  console.log(`Built ${target.GOOS}/${target.GOARCH}: ${output}`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    build();
  } catch (error) {
    console.error(error.message);
    process.exitCode = error.exitCode ?? 1;
  }
}
