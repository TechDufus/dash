import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { PluginInfo, SettingField } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { settingsPaths } from "@/settings/api";
import {
  invalidFields,
  patchResult,
  REVISION,
  settingsRoutes,
  staleRevision,
} from "@/settings/fixtures";
import { PluginSettingsPage } from "@/settings/plugin-settings-page";
import { type FetchStub, renderApp, stubFetch } from "@/test/harness";
import { host, hostsData, info, PUBLIC_KEY, sshRow, sshView, stats } from "./fixtures";
import type { HostSetting } from "./hosts";
import type { Host } from "./model.gen";

const GET = `GET ${settingsPaths.plugin("ssh")}`;
const PUT = `PUT ${settingsPaths.plugin("ssh")}`;
const HOST_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKeyOfAlpha";
const OTHER_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAnotherKey";
const offered = (key = HOST_KEY) => ({
  type: "ssh-ed25519",
  fingerprint: "SHA256:exampleHostFingerprint",
  key,
});

const ALPHA: HostSetting = { name: "alpha", address: "192.0.2.10", user: "dash" };
const BOB: HostSetting = { address: "bob.example.com:2222", user: "bob", hostKey: OTHER_KEY };

describe("the SSH settings", () => {
  let stream: TestStream;
  let stub: FetchStub;
  /** The hosts GET answers: a write that succeeds stores its list here. */
  let stored: HostSetting[];
  let revision: number;

  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const open = (
    hosts: HostSetting[],
    live: Host[],
    { routes = {}, hostsField = {}, row = sshRow(), started = true } = {} as {
      routes?: Record<string, unknown>;
      hostsField?: Partial<SettingField>;
      row?: PluginInfo;
      /** false: the plugin could not start, so it publishes no topic. */
      started?: boolean;
    },
  ) => {
    stored = hosts;
    revision = REVISION;
    stub = stubFetch(
      settingsRoutes({
        [GET]: () => sshView(stored, { hostsField, revision }),
        [PUT]: ({ body }: { body: { hosts?: HostSetting[] } }) => {
          if (body.hosts) stored = body.hosts;
          revision += 1;
          return patchResult({ revision });
        },
        ...routes,
      }),
    );
    const user = userEvent.setup();
    renderApp(<PluginSettingsPage id="ssh" />);
    stream.plugins([row]);
    if (started) stream.send("ssh.hosts", hostsData(live));
    return user;
  };

  const puts = () => stub.callsTo(PUT).map((c) => [c.headers.get("If-Match"), c.body]);
  const row = (name: string) => screen.findByRole("listitem", { name });

  it("shows dash's key and the one line that authorizes it, with a word on the user", async () => {
    open([], []);
    expect(await screen.findByText(PUBLIC_KEY)).toBeInTheDocument();
    expect(
      screen.getByText(
        `mkdir -p -m 700 ~/.ssh && echo 'restrict ${PUBLIC_KEY}' >> ~/.ssh/authorized_keys`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Copy Run on the host as the SSH user" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Give dash a user of its own with no privileges/)).toBeInTheDocument();
    expect(screen.getByText("No host yet.")).toBeInTheDocument();
  });

  it("lists the configured hosts with their live state, joined by name", async () => {
    open([ALPHA, BOB], [host("alpha", "unreachable"), host("bob.example.com")]);
    const alpha = within(await row("alpha"));
    expect(alpha.getByText("dash@192.0.2.10")).toBeInTheDocument();
    expect(alpha.getByText("Unreachable")).toBeInTheDocument();
    expect(alpha.getByText("fixed text for unreachable")).toBeInTheDocument();
    const bob = within(await row("bob.example.com"));
    expect(bob.getByText("bob@bob.example.com:2222")).toBeInTheDocument();
    expect(bob.getByText("Up")).toBeInTheDocument();
  });

  it("trusts an offered key with one merge patch of the whole list and If-Match", async () => {
    const user = open(
      [ALPHA, BOB],
      [host("alpha", "unverified", { offeredKey: offered() }), host("bob.example.com")],
    );
    const alpha = within(await row("alpha"));
    expect(alpha.getByText("SHA256:exampleHostFingerprint")).toBeInTheDocument();
    expect(alpha.getByText("ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub")).toBeInTheDocument();
    await user.click(alpha.getByRole("button", { name: "Trust this key of alpha" }));
    await waitFor(() => expect(screen.getByText("Key trusted.")).toBeInTheDocument());
    expect(puts()).toEqual([[`"${REVISION}"`, { hosts: [{ ...ALPHA, hostKey: HOST_KEY }, BOB] }]]);
  });

  it("asks before replacing a key that changed, and keeps it on Keep", async () => {
    const user = open(
      [{ ...ALPHA, hostKey: OTHER_KEY }],
      [host("alpha", "key_changed", { offeredKey: offered() })],
    );
    const alpha = within(await row("alpha"));
    expect(alpha.getByText(/offers a different key from the one dash trusts/)).toBeInTheDocument();
    await user.click(alpha.getByRole("button", { name: "Replace the trusted key… of alpha" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Only replace it if you reinstalled the host");
    await user.click(within(dialog).getByRole("button", { name: "Keep the trusted key" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(puts()).toEqual([]);

    await user.click(alpha.getByRole("button", { name: "Replace the trusted key… of alpha" }));
    await user.click(await screen.findByRole("button", { name: "Replace the key" }));
    await waitFor(() => expect(puts()).toHaveLength(1));
    expect(puts()[0]).toEqual([`"${REVISION}"`, { hosts: [{ ...ALPHA, hostKey: HOST_KEY }] }]);
  });

  it("removes a host after confirming", async () => {
    const user = open([ALPHA, BOB], [host("alpha"), host("bob.example.com")]);
    await user.click(await screen.findByRole("button", { name: "Remove alpha" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Nothing on the host changes");
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(puts()).toEqual([[`"${REVISION}"`, { hosts: [BOB] }]]));
  });

  it("says the settings changed on a 412, as the settings form does, and reads them again", async () => {
    const user = open([ALPHA], [host("alpha", "unverified", { offeredKey: offered() })], {
      routes: { [PUT]: staleRevision() },
    });
    await user.click(await screen.findByRole("button", { name: "Trust this key of alpha" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The settings changed since this page read them. It now shows the current settings: check your changes and save again.",
    );
    expect(stub.callsTo(GET).length).toBeGreaterThanOrEqual(2);
  });

  it("makes a locked list read-only and gives the hostKey line for the file", async () => {
    open([ALPHA], [host("alpha", "unverified", { offeredKey: offered() })], {
      hostsField: {
        set: true,
        source: "file",
        ref: "/data/config.yaml",
        locked: true,
        lockedBy: "file",
        stored: false,
      },
    });
    const alpha = within(await row("alpha"));
    expect(
      screen.getByText("Set in the config file /data/config.yaml. The list is read-only here."),
    ).toBeInTheDocument();
    expect(alpha.getByText(`hostKey: "${HOST_KEY}"`)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Trust this key/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Remove/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add host" })).toBeNull();
  });

  it("shows why the plugin could not start: hosts save, but nothing connects", async () => {
    open([], [], {
      row: sshRow("error", "set DASH_SECRET_KEY and a writable data directory"),
      started: false,
    });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The SSH plugin is not running");
    expect(alert).toHaveTextContent("set DASH_SECRET_KEY and a writable data directory");
    expect(alert).toHaveTextContent("dash connects to none of them until this is fixed");
    expect(screen.getByRole("button", { name: "Add host" })).toBeEnabled();
  });

  it("does not call a running plugin stopped when every host fails", async () => {
    open([ALPHA], [host("alpha", "key_changed", { offeredKey: offered(OTHER_KEY) })], {
      row: sshRow("error", "alpha: key_changed"),
    });
    await row("alpha");
    expect(screen.queryByText("The SSH plugin is not running")).toBeNull();
    expect(screen.getByRole("button", { name: /Replace the trusted key/ })).toBeEnabled();
  });

  it("keeps the switch and the interval in the generic form, without the host list", async () => {
    const user = open([ALPHA], [host("alpha")]);
    const interval = await screen.findByLabelText("intervalSeconds");
    expect(screen.getByRole("switch", { name: "enabled" })).toBeInTheDocument();
    expect(screen.queryByLabelText("hosts")).toBeNull();
    await user.clear(interval);
    await user.type(interval, "10{Enter}");
    await waitFor(() => expect(puts()).toEqual([[`"${REVISION}"`, { intervalSeconds: 10 }]]));
  });

  describe("the Add host dialog", () => {
    const field = (dialog: HTMLElement, label: string) => within(dialog).getByLabelText(label);

    it("checks the fields before anything is sent", async () => {
      const user = open([ALPHA], [host("alpha")]);
      await user.click(await screen.findByRole("button", { name: "Add host" }));
      const dialog = await screen.findByRole("dialog");
      await user.type(field(dialog, "Address"), "2001:db8::1");
      await user.click(within(dialog).getByRole("button", { name: "Save and connect" }));
      expect(within(dialog).getByRole("alert")).toHaveTextContent("Some fields need attention.");
      const address = field(dialog, "Address");
      expect(address).toHaveAttribute("aria-invalid", "true");
      expect(address).toHaveAccessibleDescription(
        expect.stringContaining("Put an IPv6 address in brackets"),
      );
      expect(field(dialog, "User")).toHaveAccessibleDescription(
        expect.stringContaining("Enter the user dash signs in as."),
      );
      expect(puts()).toEqual([]);
    });

    it("puts the server's problem under the field it names", async () => {
      const user = open([ALPHA], [host("alpha")], {
        routes: {
          [PUT]: invalidFields([
            { path: "plugins.ssh.hosts[1].address", message: "the address is not allowed" },
          ]),
        },
      });
      await user.click(await screen.findByRole("button", { name: "Add host" }));
      const dialog = await screen.findByRole("dialog");
      await user.type(field(dialog, "Address"), "192.0.2.20");
      await user.type(field(dialog, "User"), "dash");
      await user.click(within(dialog).getByRole("button", { name: "Save and connect" }));
      const address = field(dialog, "Address");
      await waitFor(() => expect(address).toHaveAttribute("aria-invalid", "true"));
      expect(address).toHaveAccessibleDescription(
        expect.stringContaining("the address is not allowed"),
      );
      expect(within(dialog).getByRole("alert")).toHaveTextContent("invalid settings");
      expect(field(dialog, "User")).not.toHaveAttribute("aria-invalid");
    });

    it("takes a new host from nothing to up as the topic follows it", async () => {
      const user = open([ALPHA], [host("alpha")]);
      await user.click(await screen.findByRole("button", { name: "Add host" }));
      const dialog = await screen.findByRole("dialog");
      await user.type(field(dialog, "Address"), "192.0.2.20");
      await user.type(field(dialog, "User"), "dash");
      await user.click(within(dialog).getByRole("button", { name: "Save and connect" }));

      const NEW: HostSetting = { address: "192.0.2.20", user: "dash" };
      await within(dialog).findByText("Adding");
      expect(puts()).toEqual([[`"${REVISION}"`, { hosts: [ALPHA, NEW] }]]);
      const current = () =>
        within(within(dialog).getByRole("list", { name: "Steps" }))
          .getAllByRole("listitem")
          .find((li) => li.getAttribute("aria-current") === "step")?.textContent;
      expect(current()).toMatch(/^Reach the host/);
      expect(within(dialog).getByText("Connecting to the host…")).toBeInTheDocument();

      const live = (h: Host) => stream.send("ssh.hosts", hostsData([host("alpha"), h]));
      live(host("192.0.2.20", "unverified", { offeredKey: offered() }));
      expect(current()).toMatch(/^Verify its key/);
      await user.click(
        within(dialog).getByRole("button", { name: "Trust this key of 192.0.2.20" }),
      );
      await waitFor(() => expect(puts()).toHaveLength(2));
      expect(puts()[1]).toEqual([
        `"${REVISION + 1}"`,
        { hosts: [ALPHA, { ...NEW, hostKey: HOST_KEY }] },
      ]);

      live(host("192.0.2.20", "unauthorized"));
      expect(current()).toMatch(/^Authorize dash/);
      expect(
        within(dialog).getByText(
          `mkdir -p -m 700 ~/.ssh && echo 'restrict ${PUBLIC_KEY}' >> ~/.ssh/authorized_keys`,
        ),
      ).toBeInTheDocument();

      live(
        host("192.0.2.20", "up", {
          info: info({
            disks: [{ name: "sda", model: "", sizeBytes: 2 ** 40, kind: "ssd" }],
            containers: {
              runtime: "podman",
              denied: false,
              items: [{ name: "web", image: "example/web", state: "running", status: "Up" }],
            },
            services: { running: 30, failed: ["backup.service"] },
          }),
          stats: stats(),
        }),
      );
      const discovered = dialog.querySelector('dl[aria-label="Discovered"]');
      if (!discovered) throw new Error("no Discovered summary");
      const found = within(discovered as HTMLElement);
      expect(found.getByText("Debian GNU/Linux 13 (trixie)")).toBeInTheDocument();
      expect(found.getByText("Example CPU · 8 CPUs")).toBeInTheDocument();
      expect(found.getByText("16.0 GiB")).toBeInTheDocument();
      expect(found.getByText("1, 1.0 TiB in all")).toBeInTheDocument();
      expect(found.getByText("1 of 1 running (podman)")).toBeInTheDocument();
      expect(found.getByText("30 running, 1 failed")).toBeInTheDocument();
      expect(within(dialog).getByRole("link", { name: "Open the host's page" })).toHaveAttribute(
        "href",
        "/hosts/192.0.2.20",
      );
      await user.click(within(dialog).getByRole("button", { name: "Done" }));
      expect(screen.queryByRole("dialog")).toBeNull();
    });
  });
});
