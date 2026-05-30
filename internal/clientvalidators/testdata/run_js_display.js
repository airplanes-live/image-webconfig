// run_js_display.js — pins the client-only altitude display + locale
// helpers shipped in web/assets/app.js. Separate from run_js_validators.js
// (which covers the JS↔bash validator parity): these two functions have no
// bash twin, they just need their behaviour locked from drift on the Go
// side. Extracts the same /* @validator-parity start … end */ block (where
// altitudeToBareMetres, which altitudeDisplayValue builds on, lives) and
// exercises imperialLengthFromLanguages + altitudeDisplayValue.
//
// Protocol (stdin → stdout, both JSON):
//   in:  { "localeCases": [{"languages": [<str>...]}],
//          "altDisplayCases": [{"metres": <str>, "imperial": <bool>}] }
//   out: { "localeResults": [{"languages": [...], "imperial": <bool>}],
//          "altDisplayResults": [{"metres": <str>, "imperial": <bool>, "output": <str>}],
//          "errors": [<str>...] }
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");

function die(message) {
    process.stderr.write(message + "\n");
    process.exit(2);
}

if (process.argv.length < 3) {
    die("usage: node run_js_display.js <path-to-app.js>");
}

let text;
try {
    text = fs.readFileSync(process.argv[2], "utf8");
} catch (e) {
    die("read app.js: " + (e && e.message ? e.message : String(e)));
}

const START = "/* @validator-parity start */";
const END = "/* @validator-parity end */";
const startIdx = text.indexOf(START);
const endIdx = text.indexOf(END);
if (startIdx === -1 || endIdx === -1 || endIdx < startIdx) {
    die("parity markers missing or out of order in " + process.argv[2]);
}
const block = text.slice(startIdx + START.length, endIdx);

const exportedNames = ["imperialLengthFromLanguages", "altitudeDisplayValue"];
const moduleSource = '"use strict";\n' + block + "\n" +
    "module.exports = { " + exportedNames.join(", ") + " };\n";

const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "display-parity-"));
const tmpModulePath = path.join(tmpDir, "block.js");
let mod;
try {
    fs.writeFileSync(tmpModulePath, moduleSource);
    mod = require(tmpModulePath);
} catch (e) {
    die("load display block: " + (e && e.message ? e.message : String(e)));
} finally {
    try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch (_) { /* best effort */ }
}

let request;
try {
    request = JSON.parse(fs.readFileSync(0, "utf8"));
} catch (e) {
    die("parse stdin JSON: " + (e && e.message ? e.message : String(e)));
}

const out = { localeResults: [], altDisplayResults: [], errors: [] };

for (const c of request.localeCases || []) {
    try {
        out.localeResults.push({
            languages: c.languages,
            imperial: Boolean(mod.imperialLengthFromLanguages(c.languages)),
        });
    } catch (e) {
        out.errors.push("imperialLengthFromLanguages(" + JSON.stringify(c.languages) + "): " + (e && e.message ? e.message : String(e)));
        out.localeResults.push({ languages: c.languages, imperial: false });
    }
}

for (const c of request.altDisplayCases || []) {
    try {
        out.altDisplayResults.push({
            metres: c.metres,
            imperial: Boolean(c.imperial),
            output: String(mod.altitudeDisplayValue(c.metres, c.imperial)),
        });
    } catch (e) {
        out.errors.push("altitudeDisplayValue(" + JSON.stringify(c.metres) + "," + JSON.stringify(c.imperial) + "): " + (e && e.message ? e.message : String(e)));
        out.altDisplayResults.push({ metres: c.metres, imperial: Boolean(c.imperial), output: "" });
    }
}

process.stdout.write(JSON.stringify(out));
