// The model behind the schema-driven plugin form (P4.9): the served JSON Schema turned into a tree
// of leaves (with $ref resolved into $defs), raw input values, their parsing, and the merge patch
// and secrets a save or a test sends. Pure functions; the components hold the state.
import type { SettingField, SettingsPluginView } from "@/api/types";

/** The subset of JSON Schema 2020-12 that internal/jsonschema emits. */
export interface JsonSchema {
  type?: string | string[];
  properties?: Record<string, JsonSchema>;
  additionalProperties?: boolean | JsonSchema;
  items?: JsonSchema;
  $ref?: string;
  $defs?: Record<string, JsonSchema>;
  enum?: unknown[];
  format?: string;
  description?: string;
  default?: unknown;
  writeOnly?: boolean;
  "x-dash-secret"?: boolean;
  "x-dash-dest"?: boolean;
  "x-dash-restart"?: boolean;
}

/** How a leaf is edited. */
export type LeafKind =
  | "string" // a text input (format: uri gives type=url semantics through the server's checks)
  | "enum" // a select
  | "integer"
  | "number"
  | "boolean" // a switch
  | "list" // an array of scalars: one item per line
  | "json" // an array of objects (or a free-form object): a validated JSON textarea
  | "secret"; // write-only: Replace and Clear, never prefilled

export interface Leaf {
  node: "leaf";
  /** The dotted path inside plugins.<id>: the secret routes' {field}, the test's secrets key. */
  rel: string;
  /** The dotted path from the root: SettingField.path and the server's error paths. */
  path: string;
  key: string;
  kind: LeafKind;
  /** The item type of a "list" leaf. */
  itemType?: "string" | "integer" | "number";
  enum?: string[];
  /** A format: uri leaf: its host and port are its destination (design D16). */
  uri: boolean;
  /** An x-dash-dest leaf: any change moves the destination (D16). */
  dest: boolean;
  doc?: string;
  default?: unknown;
  field?: SettingField;
}

export interface Group {
  node: "group";
  rel: string;
  key: string;
  doc?: string;
  children: FormNode[];
}

export type FormNode = Leaf | Group;

const MAX_DEPTH = 16;

/** `s` with a `#/$defs/<name>` reference replaced by its definition; the property's own keywords
 * (its description) win over the definition's. */
export function resolveRef(root: JsonSchema, s: JsonSchema, depth = 0): JsonSchema {
  if (!s.$ref || depth > MAX_DEPTH) return s;
  const name = s.$ref.startsWith("#/$defs/") ? s.$ref.slice("#/$defs/".length) : undefined;
  const def = name !== undefined ? root.$defs?.[name] : undefined;
  if (!def) return s;
  const { $ref: _, ...own } = s;
  return { ...resolveRef(root, def, depth + 1), ...own };
}

function typeOf(s: JsonSchema): string | undefined {
  return Array.isArray(s.type) ? s.type.find((t) => t !== "null") : s.type;
}

function kindOf(
  root: JsonSchema,
  s: JsonSchema,
  field?: SettingField,
): Pick<Leaf, "kind" | "itemType"> {
  if (s.writeOnly || s["x-dash-secret"] || field?.secret) return { kind: "secret" };
  const t = typeOf(s) ?? field?.type;
  if (t === "boolean") return { kind: "boolean" };
  if (t === "integer" || t === "number") return { kind: t };
  if (t === "string") return { kind: s.enum?.length || field?.enum?.length ? "enum" : "string" };
  if (t === "array") {
    const items = s.items ? resolveRef(root, s.items) : undefined;
    const it = items ? typeOf(items) : undefined;
    if (it === "string" || it === "integer" || it === "number")
      return { kind: "list", itemType: it };
    return { kind: "json" };
  }
  return { kind: "json" };
}

/**
 * The form tree of a plugin view: one node per property of the schema, nested objects as groups.
 * Properties follow the order of `view.fields` (the server sends them in the plugin's struct order);
 * a property without a field keeps the schema's order after them.
 */
export function buildTree(view: SettingsPluginView): FormNode[] {
  const root = view.schema as JsonSchema;
  const prefix = `plugins.${view.id}.`;
  const fields = new Map(view.fields.map((f) => [f.path, f]));
  const order = view.fields.map((f) => f.path.slice(prefix.length));

  const walk = (s: JsonSchema, base: string, depth: number): FormNode[] => {
    const props = Object.entries(s.properties ?? {});
    const rank = (rel: string) => {
      const i = order.findIndex((o) => o === rel || o.startsWith(`${rel}.`));
      return i < 0 ? order.length : i;
    };
    const nodes = props.map(([key, raw], i): { node: FormNode; rank: number; i: number } => {
      const rel = base ? `${base}.${key}` : key;
      const ps = resolveRef(root, raw);
      const field = fields.get(prefix + rel);
      if (typeOf(ps) === "object" && ps.properties && !field && depth < MAX_DEPTH) {
        return {
          node: {
            node: "group",
            rel,
            key,
            doc: ps.description,
            children: walk(ps, rel, depth + 1),
          },
          rank: rank(rel),
          i,
        };
      }
      const enumValues = (field?.enum ?? ps.enum)?.map(String);
      return {
        node: {
          node: "leaf",
          rel,
          path: prefix + rel,
          key,
          ...kindOf(root, ps, field),
          ...(enumValues?.length ? { enum: enumValues } : {}),
          uri: ps.format === "uri",
          dest: ps["x-dash-dest"] === true,
          doc: field?.doc ?? ps.description,
          default: ps.default,
          field,
        },
        rank: rank(rel),
        i,
      };
    });
    nodes.sort((a, b) => a.rank - b.rank || a.i - b.i);
    return nodes.map((n) => n.node);
  };
  return walk(root, "", 0);
}

/** Every leaf of a tree, depth first. */
export function leavesOf(nodes: readonly FormNode[]): Leaf[] {
  return nodes.flatMap((n) => (n.node === "leaf" ? [n] : leavesOf(n.children)));
}

/** What an input holds: text for everything but a switch. */
export type Raw = string | boolean;

/** The input value of a leaf as the server last answered it (its default when it sent none). */
export function initialRaw(leaf: Leaf): Raw {
  const v = leaf.field?.value !== undefined ? leaf.field.value : leaf.default;
  switch (leaf.kind) {
    case "boolean":
      return v === true;
    case "list":
      return Array.isArray(v) ? v.map(String).join("\n") : "";
    case "json":
      return v === undefined || v === null ? "" : JSON.stringify(v, null, 2);
    case "secret":
      return "";
    default:
      return v === undefined || v === null ? "" : String(v);
  }
}

export type Parsed = { ok: true; value: unknown } | { ok: false; error: string };

/** The JSON value an input stands for, or why it stands for none. */
export function parseRaw(leaf: Leaf, raw: Raw): Parsed {
  switch (leaf.kind) {
    case "boolean":
      return { ok: true, value: raw === true };
    case "integer":
    case "number": {
      const s = String(raw).trim();
      if (s === "") return { ok: true, value: null }; // null resets to the default
      const n = Number(s);
      if (!Number.isFinite(n)) return { ok: false, error: "Enter a number." };
      if (leaf.kind === "integer" && !Number.isInteger(n)) {
        return { ok: false, error: "Enter a whole number." };
      }
      return { ok: true, value: n };
    }
    case "list": {
      const items = String(raw)
        .split("\n")
        .map((l) => l.trim())
        .filter((l) => l !== "");
      if (leaf.itemType === "string" || leaf.itemType === undefined)
        return { ok: true, value: items };
      const out: number[] = [];
      for (const [i, item] of items.entries()) {
        const n = Number(item);
        if (!Number.isFinite(n) || (leaf.itemType === "integer" && !Number.isInteger(n))) {
          const what = leaf.itemType === "integer" ? "a whole number" : "a number";
          return { ok: false, error: `Line ${i + 1} is not ${what}.` };
        }
        out.push(n);
      }
      return { ok: true, value: out };
    }
    case "json": {
      const s = String(raw).trim();
      if (s === "") return { ok: true, value: null };
      let v: unknown;
      try {
        v = JSON.parse(s);
      } catch {
        return { ok: false, error: "This is not valid JSON." };
      }
      const isObject = (x: unknown) => typeof x === "object" && x !== null && !Array.isArray(x);
      if (leaf.field?.type === "object" && !isObject(v)) {
        return { ok: false, error: "Enter a JSON object." };
      }
      if (leaf.field?.type !== "object" && !(Array.isArray(v) && v.every(isObject))) {
        return { ok: false, error: "Enter a JSON list of objects." };
      }
      return { ok: true, value: v };
    }
    default:
      return { ok: true, value: String(raw) };
  }
}

/** Whether the input changes the leaf. An input that does not parse counts as a change. */
export function isChanged(leaf: Leaf, raw: Raw): boolean {
  const now = parseRaw(leaf, raw);
  if (!now.ok) return true;
  const was = parseRaw(leaf, initialRaw(leaf));
  return !was.ok || JSON.stringify(was.value ?? null) !== JSON.stringify(now.value ?? null);
}

/** Whether a pending change of this leaf moves the plugin's destination (design D16): any change of
 * a format: uri or x-dash-dest leaf. The change is the raw string against the view's (`isChanged`);
 * the browser parses no URL here, so it never disagrees with the server on what counts as a move. */
export function movesDestination(leaf: Leaf): boolean {
  return leaf.dest || leaf.uri;
}

/** A typed secret that is a Kubernetes reference rather than a value. */
export function isK8sRef(s: string): boolean {
  return s.trim().startsWith("k8s:");
}

/** `obj` with `value` at the dotted `rel` (objects created on the way). */
export function setIn(obj: Record<string, unknown>, rel: string, value: unknown): void {
  const keys = rel.split(".");
  let at = obj;
  for (const k of keys.slice(0, -1)) {
    const next = at[k];
    if (typeof next !== "object" || next === null || Array.isArray(next)) at[k] = {};
    at = at[k] as Record<string, unknown>;
  }
  at[keys[keys.length - 1] as string] = value;
}

/** The pending changes of a form: what a save writes and a test tries. */
export interface Changes {
  /** The merge patch relative to plugins.<id>, without secrets. */
  patch: Record<string, unknown>;
  /** Typed secrets by relative field: a value, or a k8s: reference. */
  secrets: Record<string, string>;
  /** A destination leaf changed (design D16). */
  destChanged: boolean;
}

export interface Collected {
  changes: Changes;
  /** Problems found before sending, by path from the root. */
  errors: Map<string, string>;
}

export const REENTER = "Re-enter this secret: the destination changed.";

/**
 * The changes of a form whose inputs are `raw` (touched leaves only) and whose typed secrets are
 * `secrets`, with the problems the browser can see: inputs that do not parse, and (D16) a set,
 * UI-writable secret that was not re-entered while a destination changes. A secret set by a locked
 * layer is left to the server, which answers 409 secret_locked naming it.
 */
export function collect(
  leaves: readonly Leaf[],
  raw: Readonly<Record<string, Raw>>,
  secrets: Readonly<Record<string, string>>,
): Collected {
  const patch: Record<string, unknown> = {};
  const errors = new Map<string, string>();
  let destChanged = false;
  for (const leaf of leaves) {
    if (leaf.kind === "secret" || leaf.field?.locked) continue;
    const r = raw[leaf.rel];
    if (r === undefined || !isChanged(leaf, r)) continue;
    const p = parseRaw(leaf, r);
    if (!p.ok) {
      errors.set(leaf.path, p.error);
      continue;
    }
    setIn(patch, leaf.rel, p.value);
    if (movesDestination(leaf)) destChanged = true;
  }
  const typed: Record<string, string> = {};
  for (const [rel, v] of Object.entries(secrets)) {
    if (v !== "") typed[rel] = v;
  }
  if (destChanged) {
    for (const leaf of leaves) {
      if (leaf.kind !== "secret" || !leaf.field?.set || leaf.field.locked) continue;
      if (typed[leaf.rel] === undefined) errors.set(leaf.path, REENTER);
    }
  }
  return { changes: { patch, secrets: typed, destChanged }, errors };
}

/** The leaf an error path belongs to: the longest leaf path that is the error path or a prefix of
 * it (an error inside a list leaf, `plugins.hardware.machines[0].name`, belongs to the list). */
export function leafForPath(leaves: readonly Leaf[], path: string): Leaf | undefined {
  let best: Leaf | undefined;
  for (const leaf of leaves) {
    const p = leaf.path;
    const under = path === p || path.startsWith(`${p}.`) || path.startsWith(`${p}[`);
    if (under && (!best || p.length > best.path.length)) best = leaf;
  }
  return best;
}
