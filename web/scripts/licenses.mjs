// Third-party licences, for the Makefile's notices and licence-check steps.
//
//   node scripts/licenses.mjs notices <go.tsv>  prints the Go and npm sections of
//                                               THIRD_PARTY_NOTICES.md
//   node scripts/licenses.mjs check             fails on an npm package whose licence is not in
//                                               ALLOWED
//
// <go.tsv> is `go-licenses report --template tools/notices/go.tpl` output: one line per Go package
// and licence (name, version, SPDX id, licence file in the module cache). The npm packages are the
// web UI's runtime dependencies (web/package.json `dependencies` and what they pull in, never
// devDependencies), as license-checker-rseidelsohn reads them from node_modules: no network and no
// install scripts are needed, so `npm ci --ignore-scripts` is enough.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { init } from "license-checker-rseidelsohn";

const web = join(fileURLToPath(new URL(".", import.meta.url)), "..");

// The SPDX licence ids an npm runtime dependency may use, each with the reason it is acceptable.
// Any other id, an id the tool only guessed (a trailing `*`), UNKNOWN or a `WITH` exception fails
// `make check`. Add an id only with a reason.
const ALLOWED = {
  MIT: "permissive",
  ISC: "permissive",
  "BSD-2-Clause": "permissive",
  "BSD-3-Clause": "permissive",
  "Apache-2.0": "permissive; a package's NOTICE file is reproduced in the notices",
  "0BSD": "permissive, no attribution required",
  "CC0-1.0": "public-domain dedication",
  Unlicense: "public-domain dedication",
  "OFL-1.1": "the bundled fonts; the SIL Open Font License allows bundling them with software",
};

function npmPackages() {
  return new Promise((resolve, reject) => {
    init({ start: web, production: true, excludePrivatePackages: true }, (err, infos) =>
      err ? reject(err) : resolve(infos),
    );
  });
}

// Minimal SPDX expression check: `A OR B` needs one allowed side, `A AND B` needs both, and
// anything it cannot parse (a `WITH` exception, a stray token) is not allowed.
function allowed(expression) {
  const tokens = expression.replace(/[()]/g, " $& ").trim().split(/\s+/);
  let i = 0;
  const atom = () => {
    const token = tokens[i++];
    if (token !== "(") {
      return Object.hasOwn(ALLOWED, token);
    }
    const value = or();
    return tokens[i++] === ")" && value;
  };
  const and = () => {
    let value = atom();
    while (tokens[i] === "AND") {
      i++;
      const right = atom();
      value = value && right;
    }
    return value;
  };
  const or = () => {
    let value = and();
    while (tokens[i] === "OR") {
      i++;
      const right = and();
      value = value || right;
    }
    return value;
  };
  const value = or();
  return i === tokens.length && value;
}

const licenceList = (info) => [info.licenses ?? "UNKNOWN"].flat();

// By code point, so the order does not depend on the locale.
const byCodePoint = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

const isLicenceFile = (file) => /^(licen[cs]e|copying)/i.test(basename(file));

const readText = (file) => readFileSync(file, "utf8").replace(/\r\n?/g, "\n").trimEnd();

function fenced(text) {
  const longest = Math.max(0, ...[...text.matchAll(/`+/g)].map((m) => m[0].length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return `${fence}text\n${text}\n${fence}`;
}

// Go modules whose licence files go-licenses classifies as one licence although they hold more.
// The yaml/v3 LICENSE is MIT for the Go port and Apache-2.0 for the files derived from libyaml;
// yaml/v2 is Apache-2.0, with LICENSE.libyaml (MIT) for the files ported from libyaml.
const GO_LICENCES = {
  "go.yaml.in/yaml/v2": ["Apache-2.0 AND MIT"],
  "go.yaml.in/yaml/v3": ["MIT AND Apache-2.0"],
  "gopkg.in/yaml.v3": ["MIT AND Apache-2.0"],
};

// Every licence file next to the one go-licenses found (go.yaml.in/yaml/v2 also has
// LICENSE.libyaml), each under its file name when there is more than one. Source files such as
// license.go are code, not licence texts.
const isSource = (file) => /\.(go|s|c|h|py|js|ts|sh)$/i.test(file);

function goLicenceText(file) {
  const dir = dirname(file);
  const files = readdirSync(dir)
    .filter((f) => isLicenceFile(f) && !isSource(f) && statSync(join(dir, f)).isFile())
    .sort(byCodePoint);
  if (files.length <= 1) {
    return readText(file);
  }
  return files.map((f) => `==> ${f} <==\n\n${readText(join(dir, f))}`).join("\n\n");
}

// A package: { name, version, licences, text, notice, author }; text is null when the package has
// no licence file.
function goEntries(tsv) {
  const entries = new Map();
  const lines = readFileSync(tsv, "utf8").split("\n").filter(Boolean).sort(byCodePoint);
  for (const line of lines) {
    const [name, version, licence, path] = line.split("\t");
    const known = entries.get(name);
    if (known) {
      known.licences.push(licence);
      continue;
    }
    const file = path.startsWith("/") && isLicenceFile(path) ? path : null;
    const noticeName = file && readdirSync(dirname(file)).find((f) => /^notice(\.\w+)?$/i.test(f));
    entries.set(name, {
      name,
      version,
      licences: [licence],
      text: file && goLicenceText(file),
      notice: noticeName ? readText(join(dirname(file), noticeName)) : null,
    });
  }
  for (const entry of entries.values()) {
    const fixed = GO_LICENCES[entry.name];
    if (fixed && entry.licences.length === 1) {
      entry.licences = fixed;
    }
  }
  return [...entries.values()];
}

function npmEntries(infos) {
  return Object.keys(infos)
    .sort(byCodePoint)
    .map((id) => {
      const info = infos[id];
      const at = id.lastIndexOf("@");
      const file = info.licenseFile && isLicenceFile(info.licenseFile) ? info.licenseFile : null;
      return {
        name: id.slice(0, at),
        version: id.slice(at + 1),
        licences: licenceList(info),
        text: file && readText(file),
        notice: info.noticeFile ? readText(info.noticeFile) : null,
        author: info.publisher,
      };
    });
}

// Packages with the same licence text (and NOTICE) share one copy of it.
function section(title, intro, entries) {
  const groups = new Map();
  for (const entry of entries) {
    const key = entry.text === null ? `\0${entry.name}` : `${entry.text}\0${entry.notice ?? ""}`;
    groups.set(key, [...(groups.get(key) ?? []), entry]);
  }
  const out = [`## ${title}`, "", ...intro];
  for (const group of groups.values()) {
    const [first] = group;
    const more = group.length > 1 ? ` and ${group.length - 1} more` : "";
    out.push("", `### ${first.name}${more}`, "");
    for (const entry of group) {
      out.push(`- ${entry.name} ${entry.version}: ${entry.licences.join(", ")}`);
    }
    out.push("");
    if (first.text === null) {
      const by = first.author ? ` Author: ${first.author}.` : "";
      out.push(`The package ships no licence file; its metadata declares the licence above.${by}`);
    } else {
      out.push(fenced(first.text));
    }
    if (first.notice) {
      out.push("", "NOTICE:", "", fenced(first.notice));
    }
  }
  return out;
}

function notices(goTSV, infos) {
  const out = [
    ...section(
      "Go modules",
      [
        "The Go packages compiled into the `dash` binary (`./cmd/dash`, for linux), as",
        "[go-licenses](https://github.com/google/go-licenses) finds them in the module cache.",
      ],
      goEntries(goTSV),
    ),
    "",
    ...section(
      "npm packages",
      [
        "The web UI's runtime npm dependencies (`dependencies` in web/package.json and theirs), as",
        "[license-checker-rseidelsohn](https://github.com/RSeidelsohn/license-checker-rseidelsohn)",
        "reads them from web/node_modules. The web assets that the `dash` binary embeds are built",
        "from them.",
      ],
      npmEntries(infos),
    ),
  ];
  return `${out.join("\n")}\n`;
}

function check(infos) {
  const ids = Object.keys(infos).sort(byCodePoint);
  const bad = ids.filter((id) => !licenceList(infos[id]).every(allowed));
  if (bad.length > 0) {
    console.error(
      `licenses: ${bad.length} runtime npm package(s) under a licence outside the allowlist (ALLOWED in web/scripts/licenses.mjs):\n` +
        bad.map((id) => `- ${id}: ${licenceList(infos[id]).join(", ")}`).join("\n") +
        "\nReplace the dependency, or allow the licence id there with a reason.",
    );
    process.exit(1);
  }
  console.log(`licenses: ${ids.length} runtime npm packages, all under allowed licences`);
}

const [mode, goTSV] = process.argv.slice(2);
if (!(mode === "check" || (mode === "notices" && goTSV))) {
  console.error("usage: node scripts/licenses.mjs notices <go.tsv> | check");
  process.exit(2);
}
const infos = await npmPackages();
if (mode === "notices") {
  process.stdout.write(notices(goTSV, infos));
} else {
  check(infos);
}
