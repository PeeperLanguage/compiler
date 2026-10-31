// Runs the built checker as a page would, with Node's WASI. It proves what the
// native tests cannot: that wasm loads two files with no threads, finds the core
// library inside the wasm file, and survives a runaway recursion in a constant.
// node crates/driver/browser/smoke.mjs <browser.wasm>
import assert from "node:assert/strict";
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WASI } from "node:wasi";

const module = await WebAssembly.compile(readFileSync(process.argv[2]));

// What a check of `files` finds, in a new instance, as a page runs it.
async function check(files) {
    const folder = mkdtempSync(join(tmpdir(), "peeper-browser-"));
    const input = join(folder, "input.json");
    const output = join(folder, "output.json");
    writeFileSync(input, JSON.stringify(files));
    const stdin = openSync(input, "r");
    const stdout = openSync(output, "w");
    const wasi = new WASI({ version: "preview1", stdin, stdout, returnOnExit: true });
    const code = wasi.start(await WebAssembly.instantiate(module, wasi.getImportObject()));
    closeSync(stdin);
    closeSync(stdout);
    const found = JSON.parse(readFileSync(output, "utf8"));
    rmSync(folder, { recursive: true });
    assert.equal(code, 0);
    return found;
}

const [mismatch] = await check({
    "main.peep": 'import "playground:other";\nfn main() {\n    other::Value();\n}\n',
    "other.peep": "fn Value() -> i32 {\n    return true;\n}\n",
});
assert.equal(mismatch.code, "T0001");
assert.equal(mismatch.place.file, "other.peep");
assert.deepEqual(mismatch.place.start, { line: 2, column: 12 });
assert.deepEqual(mismatch.place.end, { line: 2, column: 16 });

// Past wasm's stack this would end the instance; `eval` must stop it first.
const [recursion] = await check({
    "main.peep": "fn f(n: i32) -> i32 {\n    return f(n + 1);\n}\nconst X: i32 = f(0);\nfn main() {}\n",
});
assert.equal(recursion.code, "T0044");
assert.equal(recursion.related[0].message, "this went 500 calls deep");
console.log("the wasm checker found both errors");
