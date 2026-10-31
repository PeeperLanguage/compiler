// Runs the built checker as a page would, with Node's WASI. It proves what the
// native tests cannot: that wasm loads two files with no threads, finds the core
// library inside the wasm file, survives a runaway recursion in a constant, and
// runs a program.
// node crates/driver/browser/smoke.mjs <browser.wasm>
import assert from "node:assert/strict";
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WASI } from "node:wasi";

const module = await WebAssembly.compile(readFileSync(process.argv[2]));

// What `request` gives, in a new instance, as a page runs it.
async function send(request) {
    const folder = mkdtempSync(join(tmpdir(), "peeper-browser-"));
    const input = join(folder, "input.json");
    const output = join(folder, "output.json");
    writeFileSync(input, JSON.stringify(request));
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

const check = (files) => send({ files });

const [mismatch] = await check({
    "main.peep": 'import "playground:other";\nfn main() {\n    other::Value();\n}\n',
    "other.peep": "fn Value() -> i32 {\n    return true;\n}\n",
});
assert.equal(mismatch.code, "T0001");
assert.equal(mismatch.place.file, "other.peep");
assert.deepEqual(mismatch.place.start, { line: 2, column: 12 });
assert.deepEqual(mismatch.place.end, { line: 2, column: 16 });

// A run's calls use no host stack, so this is the same error as natively.
const [recursion] = await check({
    "main.peep": "fn f(n: i32) -> i32 {\n    return f(n + 1);\n}\nconst X: i32 = f(0);\nfn main() {}\n",
});
assert.equal(recursion.code, "T0044");
assert.equal(recursion.related[0].message, "this went 100000 calls deep");

// An address past 32 bits must not wrap round to memory the run owns.
const [wrapped] = await check({
    "main.peep":
        'fn text() -> cstr {\n    return c"abcd";\n}\nfn f() -> i32 {\n    let held = text();\n    let mut far: u64 = 5368709120;\n    let p = @far as **i32;\n    return p.*.*;\n}\nconst X: i32 = f();\nfn main() {}\n',
});
assert.equal(wrapped.code, "T0044");
assert.equal(wrapped.related[0].message, "this reads or writes memory the program does not own");

// A program runs with what it printed, with no backend or linker.
const { diagnostics, run } = await send({
    run: true,
    files: {
        "main.peep":
            'struct Point { x: i32, y: f64 }\nfn main() -> i32 {\n    println(Point.{ x = 1, y = 2.5 }, [2]i32{3, 4}, 340282366920938463463374607431768211455u128);\n    return 7;\n}\n',
    },
});
assert.equal(diagnostics, "");
assert.deepEqual(run, {
    output: [
        {
            stream: "stdout",
            text: "Point { x: 1, y: 2.5 } [3, 4] 340282366920938463463374607431768211455\n",
        },
    ],
    exit_code: 7,
});
const stopped = await send({ run: true, files: { "main.peep": 'fn main() {\n    panic "stop";\n}\n' } });
assert.equal(stopped.run.exit_code, 101);
assert.deepEqual(stopped.run.output, [{ stream: "stderr", text: "panic: stop\n  --> main.peep:2:5\n" }]);
const failed = await send({ run: true, files: { "main.peep": "fn main() {\n    let x: i32 = true;\n}\n" } });
assert.match(failed.diagnostics, /\[T0001\]/);
assert.match(failed.diagnostics, /main\.peep:2:18/);
assert.equal(failed.run, null);
console.log("the wasm checker found every error and ran the programs");
