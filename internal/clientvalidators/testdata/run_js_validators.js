#!/usr/bin/env node
//
// run_js_validators.js — extracts the /* @validator-parity */ block from
// web/assets/app.js, evaluates it in an isolated CommonJS module, and runs
// boolean validators + altitudeToBareMetres against test vectors supplied
// as JSON on stdin. Results go to stdout as JSON.
//
// Invocation:
//   echo '{"vectors":[…], "altitudeCases":[…]}' | \
//     node run_js_validators.js /abs/path/to/app.js
//
// Output schema (stdout):
//   {
//     "results": [{"validator": <str>, "input": <str>, "ok": <bool>}],
//     "altitudeResults": [{"input": <str>, "output": <str|null>, "ok": <bool>}],
//     "errors": [<str>]                              // non-fatal per-vector errors
//   }
//
// Driven by internal/clientvalidators/parity_test.go. Keep the exported
// validator-name set in sync with the block's symbol set.

"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");

function die(message) {
    process.stderr.write(message + "\n");
    process.exit(2);
}

if (process.argv.length < 3) {
    die("usage: node run_js_validators.js <path-to-app.js>");
}

const appJsPath = process.argv[2];
let text;
try {
    text = fs.readFileSync(appJsPath, "utf8");
} catch (e) {
    die("read app.js: " + (e && e.message ? e.message : String(e)));
}

const START = "/* @validator-parity start */";
const END = "/* @validator-parity end */";
const startIdx = text.indexOf(START);
const endIdx = text.indexOf(END);
if (startIdx === -1 || endIdx === -1 || endIdx < startIdx) {
    die("parity markers missing or out of order in " + appJsPath);
}
const block = text.slice(startIdx + START.length, endIdx);

// Names exported from the block. Must match the symbols declared inside
// /* @validator-parity start ... end */ in app.js.
const exportedNames = [
    "isValidLatitude",
    "isValidLongitude",
    "isValidAltitude",
    "altitudeToBareMetres",
    "isValidMlatUser",
    "isValidGain",
    "isValidReadsbSdrSerial",
    "isValidDump978Serial",
    "isValidDump978Gain",
    "isValidWifiSSID",
    "isValidWifiPSK",
    "isValidWifiCountry",
    "isValidWifiPriority",
    "isValidAggEmail",
    "isValidFr24Key",
    "isValidFeederId",
];

const moduleSource = '"use strict";\n' +
    block + "\n" +
    "module.exports = { " + exportedNames.join(", ") + " };\n";

const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "validator-parity-"));
const tmpModulePath = path.join(tmpDir, "block.js");
let validators;
try {
    fs.writeFileSync(tmpModulePath, moduleSource);
    validators = require(tmpModulePath);
} catch (e) {
    die("load validator block: " + (e && e.message ? e.message : String(e)));
} finally {
    try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch (_) { /* best effort */ }
}

let stdinRaw;
try {
    stdinRaw = fs.readFileSync(0, "utf8");
} catch (e) {
    die("read stdin: " + (e && e.message ? e.message : String(e)));
}

let request;
try {
    request = JSON.parse(stdinRaw);
} catch (e) {
    die("parse stdin JSON: " + (e && e.message ? e.message : String(e)));
}

const out = { results: [], altitudeResults: [], errors: [] };

for (const v of request.vectors || []) {
    const name = v.validator;
    const fn = validators[name];
    if (typeof fn !== "function") {
        out.errors.push("unknown validator: " + name);
        out.results.push({ validator: name, input: v.input, ok: false });
        continue;
    }
    try {
        const result = fn(v.input);
        out.results.push({ validator: name, input: v.input, ok: Boolean(result) });
    } catch (e) {
        out.errors.push(name + "(" + JSON.stringify(v.input) + "): " + (e && e.message ? e.message : String(e)));
        out.results.push({ validator: name, input: v.input, ok: false });
    }
}

for (const c of request.altitudeCases || []) {
    try {
        const result = validators.altitudeToBareMetres(c.input);
        out.altitudeResults.push({
            input: c.input,
            output: result === null ? null : String(result),
            ok: result !== null,
        });
    } catch (e) {
        out.errors.push("altitudeToBareMetres(" + JSON.stringify(c.input) + "): " + (e && e.message ? e.message : String(e)));
        out.altitudeResults.push({ input: c.input, output: null, ok: false });
    }
}

process.stdout.write(JSON.stringify(out));
