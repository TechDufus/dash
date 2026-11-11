import { describe, expect, it } from "vitest";
import {
  authorizeCommand,
  draftSetting,
  effectiveName,
  fingerprintCommand,
  hostPart,
  MAX_HOSTS,
  validateDraft,
} from "./hosts";

const draft = (address: string, user = "dash", name = "") => ({ address, user, name });

describe("host names", () => {
  it.each([
    ["alpha.example.com", "alpha.example.com"],
    ["alpha.example.com:2222", "alpha.example.com"],
    ["192.0.2.10", "192.0.2.10"],
    ["192.0.2.10:22", "192.0.2.10"],
    ["[2001:db8::1]", "2001:db8::1"],
    ["[2001:db8::1]:2222", "2001:db8::1"],
  ])("takes the host part of %s", (address, want) => {
    expect(hostPart(address)).toBe(want);
  });

  it("names a host by its name, else by its address's host", () => {
    expect(effectiveName({ name: "alpha", address: "192.0.2.10" })).toBe("alpha");
    expect(effectiveName({ name: "  ", address: "192.0.2.10:22" })).toBe("192.0.2.10");
    expect(effectiveName({ address: "bob.example.com" })).toBe("bob.example.com");
  });
});

describe("validateDraft", () => {
  it("accepts a host with or without a port and a name", () => {
    expect(validateDraft(draft("192.0.2.10"), []).size).toBe(0);
    expect(validateDraft(draft("[2001:db8::1]:2222", "_svc$", "bob"), []).size).toBe(0);
  });

  it.each([
    ["", "Enter the host's address."],
    ["2001:db8::1", "Put an IPv6 address in brackets, as in [2001:db8::1] or [2001:db8::1]:22."],
    ["alpha:0", "The port must be a number from 1 to 65535."],
    ["alpha:65536", "The port must be a number from 1 to 65535."],
    ["alpha:ssh", "The port must be a number from 1 to 65535."],
    ["alice@alpha", "Enter a host name or an IP address, optionally with :port."],
    [":22", "Enter a host name or an IP address, optionally with :port."],
  ])("refuses the address %j", (address, message) => {
    expect(validateDraft(draft(address), []).get("address")).toBe(message);
  });

  it("refuses a user the server's pattern refuses, without repeating it", () => {
    expect(validateDraft(draft("alpha", ""), []).get("user")).toBe(
      "Enter the user dash signs in as.",
    );
    for (const user of ["Alice", "1dash", "a".repeat(33), "da sh"]) {
      const message = validateDraft(draft("alpha", user), []).get("user");
      expect(message, user).toMatch(/^A user name starts with/);
      expect(message).not.toContain(user);
    }
  });

  it("refuses a name that is too long, has a slash or a control character", () => {
    expect(validateDraft(draft("alpha", "dash", "é".repeat(33)), []).get("name")).toBe(
      "At most 64 bytes.",
    );
    expect(validateDraft(draft("alpha", "dash", "a/b"), []).get("name")).toBe(
      "A name cannot contain /.",
    );
    expect(validateDraft(draft("alpha", "dash", "a\tb"), []).get("name")).toBe(
      "A name cannot contain control characters.",
    );
  });

  it("refuses a name another host has, ignoring case, typed or taken from the address", () => {
    const others = [
      { name: "Alpha", address: "192.0.2.10", user: "dash" },
      { address: "bob", user: "dash" },
    ];
    expect(validateDraft(draft("192.0.2.11", "dash", "alpha"), others).get("name")).toBe(
      "Another host has this name.",
    );
    expect(validateDraft(draft("BOB:22"), others).get("name")).toBe(
      "Another host has this address's name: give this one a name.",
    );
    expect(validateDraft(draft("bob:22", "dash", "bob-2"), others).size).toBe(0);
  });

  it("refuses a 65th host", () => {
    const others = Array.from({ length: MAX_HOSTS }, (_, i) => ({
      address: `192.0.2.${i + 1}`,
      user: "dash",
    }));
    expect(validateDraft(draft("198.51.100.1"), others).get("hosts")).toBe(
      "dash reads at most 64 hosts.",
    );
  });

  it("saves a draft trimmed, without an empty name", () => {
    expect(draftSetting(draft(" 192.0.2.10 ", " dash "))).toEqual({
      address: "192.0.2.10",
      user: "dash",
    });
    expect(draftSetting(draft("192.0.2.10", "dash", " alpha "))).toEqual({
      name: "alpha",
      address: "192.0.2.10",
      user: "dash",
    });
  });
});

describe("the commands an administrator runs", () => {
  it("authorizes dash's key with restrict, creating ~/.ssh with mode 700 when missing", () => {
    expect(authorizeCommand("ssh-ed25519 AAAA dash")).toBe(
      "mkdir -p -m 700 ~/.ssh && echo 'restrict ssh-ed25519 AAAA dash' >> ~/.ssh/authorized_keys",
    );
  });

  it("keeps a quote in the key inside the shell's quotes", () => {
    expect(authorizeCommand("ssh-ed25519 AAAA it's")).toBe(
      "mkdir -p -m 700 ~/.ssh && echo 'restrict ssh-ed25519 AAAA it'\\''s' >> ~/.ssh/authorized_keys",
    );
  });

  it("compares the fingerprint of the host key file of the offered type", () => {
    expect(fingerprintCommand("ssh-ed25519")).toBe(
      "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub",
    );
    expect(fingerprintCommand("ecdsa-sha2-nistp256")).toBe(
      "ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub",
    );
    expect(fingerprintCommand("rsa-sha2-512")).toBe("ssh-keygen -lf /etc/ssh/ssh_host_rsa_key.pub");
  });
});
