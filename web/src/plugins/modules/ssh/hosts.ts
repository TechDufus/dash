// What the Hosts page and the SSH settings share about a host: its state in words, its name as
// dash derives it, the checks the settings run before a write, and the commands an administrator
// runs on a host. Pure functions; the components hold the state.
import type { Host, HostState } from "./model.gen";

/** One entry of plugins.ssh.hosts, as the settings view holds it. */
export interface HostSetting {
  name?: string;
  address: string;
  user: string;
  hostKey?: string;
}

const STATE_LABEL: Readonly<Record<string, string>> = {
  connecting: "Connecting",
  unverified: "Key not trusted",
  key_changed: "Host key changed",
  unauthorized: "Not authorized",
  unreachable: "Unreachable",
  unsupported: "Not Linux",
  up: "Up",
};

/** A state in a few words; a state this build does not know stands as it is. */
export function stateLabel(state: HostState): string {
  return STATE_LABEL[state] ?? state;
}

/** States an administrator ends in the settings: trust the key, or authorize dash's key. */
export function needsSetup(state: HostState): boolean {
  return state === "unverified" || state === "key_changed" || state === "unauthorized";
}

/**
 * The host part of an address: host, host:port, [v6] or [v6]:port. An address that does not
 * parse stands as it is (the server refuses it anyway).
 */
export function hostPart(address: string): string {
  const a = address.trim();
  if (a.startsWith("[")) {
    const end = a.indexOf("]");
    return end > 1 ? a.slice(1, end) : a;
  }
  const colon = a.indexOf(":");
  return colon >= 0 && colon === a.lastIndexOf(":") ? a.slice(0, colon) : a;
}

/** The name dash shows a configured host under: its name, else the host part of its address. */
export function effectiveName(h: Pick<HostSetting, "name" | "address">): string {
  const name = h.name?.trim() ?? "";
  return name !== "" ? name : hostPart(h.address);
}

/** Whether two host names are the same host (names are unique ignoring case). */
export function sameName(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}

/** The host of the topic a configured host is: by exact name first, then ignoring case. */
export function liveHost(hosts: readonly Host[] | undefined, name: string): Host | undefined {
  if (!hosts) return undefined;
  return hosts.find((h) => h.name === name) ?? hosts.find((h) => sameName(h.name, name));
}

/**
 * The host a route names. The router decodes the path with decodeURI, which leaves the escapes of
 * reserved characters a card link puts in a name, so the route is also decoded as a component; a
 * literal "%" that is no escape makes that throw, and the route then stands as it is.
 */
export function findHost(hosts: readonly Host[], route: string): Host | undefined {
  let decoded = route;
  try {
    decoded = decodeURIComponent(route);
  } catch {
    // A "%" not followed by two hex digits: the name itself.
  }
  return hosts.find((h) => h.name === route || h.name === decoded);
}

/** The page of one host. */
export function hostPath(name: string): string {
  return `/hosts/${encodeURIComponent(name)}`;
}

/** Where an administrator finishes a host's setup. */
export const SETTINGS_PATH = "/settings/plugins/ssh";

/** The most hosts the plugin reads (internal/pluginconf/ssh.go). */
export const MAX_HOSTS = 64;

const USER = /^[a-z_][a-z0-9_.-]{0,31}\$?$/;
// biome-ignore lint/suspicious/noControlCharactersInRegex: control characters are what it finds
const CONTROL = /[\u0000-\u001f\u007f]/;

/** A host's fields as the Add host dialog holds them. */
export interface HostDraft {
  name: string;
  address: string;
  user: string;
}

/** Why an address does not parse as host[:port], or undefined when it does. */
function addressProblem(address: string): string | undefined {
  const a = address.trim();
  if (a === "") return "Enter the host's address.";
  let host: string;
  let port: string | undefined;
  const v6 = /^\[([^\]]+)\](?::(.*))?$/.exec(a);
  if (v6) {
    host = v6[1] ?? "";
    port = v6[2];
  } else if (a.indexOf(":") !== a.lastIndexOf(":")) {
    return "Put an IPv6 address in brackets, as in [2001:db8::1] or [2001:db8::1]:22.";
  } else {
    const [h, p] = a.split(":");
    host = h ?? "";
    port = p;
  }
  if (host === "" || /[\s/@]/.test(host)) {
    return "Enter a host name or an IP address, optionally with :port.";
  }
  if (port !== undefined) {
    const n = /^\d{1,5}$/.test(port) ? Number(port) : Number.NaN;
    if (!(n >= 1 && n <= 65535)) return "The port must be a number from 1 to 65535.";
  }
  return undefined;
}

/**
 * The problems of a new host against the hosts already configured, by field (`name`, `address`,
 * `user`, or `hosts` for the list), mirroring the server's checks so most typos never leave the
 * page. A message never repeats a value.
 */
export function validateDraft(
  draft: HostDraft,
  others: readonly HostSetting[],
): Map<keyof HostDraft | "hosts", string> {
  const errors = new Map<keyof HostDraft | "hosts", string>();
  if (others.length >= MAX_HOSTS) errors.set("hosts", `dash reads at most ${MAX_HOSTS} hosts.`);
  const address = addressProblem(draft.address);
  if (address) errors.set("address", address);
  const user = draft.user.trim();
  if (user === "") errors.set("user", "Enter the user dash signs in as.");
  else if (!USER.test(user)) {
    errors.set(
      "user",
      "A user name starts with a lower-case letter or _, followed by at most 31 of a-z, 0-9, _, . and -.",
    );
  }
  const name = draft.name.trim();
  if (name !== "") {
    if (new TextEncoder().encode(name).length > 64) errors.set("name", "At most 64 bytes.");
    else if (name.includes("/")) errors.set("name", "A name cannot contain /.");
    else if (CONTROL.test(name)) errors.set("name", "A name cannot contain control characters.");
  }
  if (!errors.has("name") && !errors.has("address")) {
    const mine = effectiveName(draft);
    if (others.some((o) => sameName(effectiveName(o), mine))) {
      if (name !== "") errors.set("name", "Another host has this name.");
      else errors.set("name", "Another host has this address's name: give this one a name.");
    }
  }
  return errors;
}

/** The host entry a valid draft saves: an empty name is left out (the address names the host). */
export function draftSetting(draft: HostDraft): HostSetting {
  const name = draft.name.trim();
  return {
    ...(name !== "" ? { name } : {}),
    address: draft.address.trim(),
    user: draft.user.trim(),
  };
}

/** `s` quoted for a POSIX shell: inside single quotes, each ' written as '\''. */
function shellQuote(s: string): string {
  return `'${s.replaceAll("'", "'\\''")}'`;
}

/**
 * The line an administrator runs on the host as the SSH user: it creates ~/.ssh with mode 700
 * when it is missing and appends dash's key with `restrict` (no forwarding, no pty, no rc file).
 */
export function authorizeCommand(publicKey: string): string {
  return `mkdir -p -m 700 ~/.ssh && echo ${shellQuote(`restrict ${publicKey.trim()}`)} >> ~/.ssh/authorized_keys`;
}

/** The host key file of a key type, as OpenSSH names it. */
export function hostKeyFile(type: string): string {
  if (type.startsWith("ecdsa-")) return "/etc/ssh/ssh_host_ecdsa_key.pub";
  if (type === "ssh-rsa" || type.startsWith("rsa-")) return "/etc/ssh/ssh_host_rsa_key.pub";
  return "/etc/ssh/ssh_host_ed25519_key.pub";
}

/** The command that prints the fingerprint of the host's own key, to compare with the offered one. */
export function fingerprintCommand(type: string): string {
  return `ssh-keygen -lf ${hostKeyFile(type)}`;
}
