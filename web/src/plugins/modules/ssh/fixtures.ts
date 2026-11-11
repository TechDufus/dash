// Builders of the ssh plugin's wire shapes for the tests: the ssh.hosts topic, the plugin row and
// GET /api/v1/settings/plugins/ssh with the schema internal/jsonschema reflects for plugins.ssh.
import type { PluginInfo, SettingField, SettingsPluginView } from "@/api/types";
import { field, REVISION } from "@/settings/fixtures";
import { pluginInfo } from "@/test/harness";
import type { HostSetting } from "./hosts";
import type { Host, HostsData, Info, Stats } from "./model.gen";

export const PUBLIC_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExample dash";

/** The ssh plugin row, as /api/v1/plugins lists it. */
export function sshRow(state = "healthy", message = ""): PluginInfo {
  return pluginInfo({
    id: "ssh",
    name: "SSH hosts",
    state,
    message,
    topics: ["ssh.hosts"],
    nav: [{ path: "/hosts", label: "Hosts", title: "Hosts", icon: "Server" }],
    configurable: true,
  });
}

export function stats(over: Partial<Stats> = {}): Stats {
  return {
    sampledAt: "2026-11-11T10:00:00Z",
    cpuRatio: 0.25,
    ioWaitRatio: 0.01,
    load1: 0.5,
    load5: 0.75,
    load15: 1,
    memUsedBytes: 4 * 2 ** 30,
    memTotalBytes: 16 * 2 ** 30,
    swapUsedBytes: 0,
    swapTotalBytes: 0,
    uptimeSeconds: 90_000,
    netRxBytesPerSec: 1024,
    netTxBytesPerSec: 2048,
    diskReadBytesPerSec: 4096,
    diskWriteBytesPerSec: 8192,
    cpuTempC: 54,
    sensors: [],
    fans: [],
    ...over,
  };
}

export function info(over: Partial<Info> = {}): Info {
  return {
    hostname: "alpha",
    os: "Debian GNU/Linux 13 (trixie)",
    kernel: "6.12.0-1-amd64",
    arch: "x86_64",
    vendor: "",
    product: "",
    virtualization: "",
    cpuModel: "Example CPU",
    cpus: 8,
    disks: [],
    filesystems: [],
    pools: [],
    ...over,
  };
}

/** A host in `state`; an up host has stats and an inventory. */
export function host(name: string, state = "up", over: Partial<Host> = {}): Host {
  const up = state === "up";
  return {
    ref: `ssh:Host/${name}`,
    name,
    state,
    message: up ? "" : `fixed text for ${state}`,
    health: up ? "ok" : state === "key_changed" ? "crit" : "unknown",
    since: "2026-11-11T09:00:00Z",
    ...(up ? { info: info({ hostname: name }), stats: stats() } : {}),
    ...over,
  };
}

export function hostsData(hosts: Host[], over: Partial<HostsData> = {}): HostsData {
  return {
    hosts,
    identity: { publicKey: PUBLIC_KEY, fingerprint: "SHA256:exampleDashFingerprint" },
    intervalSeconds: 5,
    ...over,
  };
}

/** The schema of plugins.ssh (a host list, the interval, the switch, the type). */
export const sshSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  additionalProperties: false,
  type: "object",
  properties: {
    enabled: { type: "boolean", description: "false turns the plugin off." },
    hosts: {
      type: "array",
      description: "The Linux machines dash reads over SSH. A JSON list in the environment.",
      items: { $ref: "#/$defs/SSHHost" },
    },
    intervalSeconds: {
      type: "integer",
      default: 5,
      description: "How often a host that is up is sampled, 2 to 60 seconds.",
    },
    type: { type: "string", description: "The plugin type." },
  },
  $defs: {
    SSHHost: {
      type: "object",
      additionalProperties: false,
      properties: {
        name: { type: "string" },
        address: { type: "string" },
        user: { type: "string" },
        hostKey: { type: "string" },
      },
    },
  },
};

/** GET /api/v1/settings/plugins/ssh with `hosts` saved from the UI; `hostsField` overrides that field. */
export function sshView(
  hosts: HostSetting[],
  over: { hostsField?: Partial<SettingField>; revision?: number } = {},
): SettingsPluginView {
  const S = "plugins.ssh.";
  return {
    id: "ssh",
    schema: sshSchema,
    fields: [
      field(`${S}enabled`, "boolean"),
      field(`${S}type`, "string", { value: "" }),
      field(`${S}hosts`, "array", {
        value: hosts,
        set: hosts.length > 0,
        source: hosts.length > 0 ? "ui" : "default",
        stored: hosts.length > 0,
        ...over.hostsField,
      }),
      field(`${S}intervalSeconds`, "integer", { value: 5 }),
    ],
    revision: over.revision ?? REVISION,
    testable: false,
  };
}
