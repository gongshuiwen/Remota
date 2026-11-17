import assert from "node:assert/strict";
import childProcess from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { build } from "./build.mjs";

for (const failed of [true, false]) {
  test(
    failed
      ? "second binary failure preserves the previous pair"
      : "successful build replaces both binaries",
    (t) => {
      const base = resolve(tmpdir());
      const root = mkdtempSync(join(base, "remota-build-"));
      t.after(() => {
        if (dirname(root) !== base)
          throw new Error("Unexpected test directory");
        rmSync(root, { recursive: true, force: true });
      });
      const output = join(root, "dist", "linux-amd64");
      mkdirSync(output, { recursive: true });
      const names = ["remota-server", "remota-agent"];
      for (const name of names)
        writeFileSync(join(output, name), `old ${name}`);
      t.mock.method(childProcess, "execFileSync", () =>
        JSON.stringify({ GOOS: "linux", GOARCH: "amd64" }),
      );
      t.mock.method(childProcess, "spawnSync", (command, args) => {
        if (command === "go") {
          const name = args.at(-1).split("/").at(-1);
          if (failed && name === "remota-agent") return { status: 7 };
          writeFileSync(args[args.indexOf("-o") + 1], `new ${name}`);
        }
        return { status: 0 };
      });
      if (failed) assert.throws(() => build(root), { exitCode: 7 });
      else build(root);
      for (const name of names)
        assert.equal(
          readFileSync(join(output, name), "utf8"),
          `${failed ? "old" : "new"} ${name}`,
        );
      assert.deepEqual(readdirSync(join(root, "dist")), ["linux-amd64"]);
    },
  );
}
