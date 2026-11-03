import { beforeEach, describe, expect, it, type Mock, vi } from "vitest";
import {
  handle401,
  isPublicPath,
  MAX_NEXT_LENGTH,
  type Nav,
  REAUTH_KEY,
  REAUTH_WINDOW_MS,
  safeNext,
  validNext,
} from "./handle401";

const ORIGIN = window.location.origin;
const T0 = new Date("2026-01-01T00:00:00Z").getTime();

interface FakeNav extends Nav {
  reload: Mock<() => void>;
  assign: Mock<(url: string) => void>;
}

function fakeNav(): FakeNav {
  return { reload: vi.fn<() => void>(), assign: vi.fn<(url: string) => void>() };
}

const at = (pathname: string, search = "", hash = "") => ({ pathname, search, hash });
const PODS = at("/pods", "?ns=media");
const unauth = (details?: unknown) => ({ status: 401, code: "unauthenticated", details });

/** What one handle401 call did: "reload", the assigned URL, or "none". */
function outcome(nav: FakeNav): string {
  const calls = nav.reload.mock.calls.length + nav.assign.mock.calls.length;
  if (calls > 1) return `more than one action (${calls})`;
  if (nav.reload.mock.calls.length) return "reload";
  return nav.assign.mock.calls[0]?.[0] ?? "none";
}

function run(err: Parameters<typeof handle401>[0], loc = PODS): string {
  const nav = fakeNav();
  handle401(err, nav, loc);
  return outcome(nav);
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(T0);
});

describe("handle401: the mode matrix", () => {
  const NEXT = encodeURIComponent("/pods?ns=media");
  it.each([
    ["proxy", { mode: "proxy", setup: false }, "reload"],
    ["local", { mode: "local", setup: false }, `/login?next=${NEXT}`],
    ["local before setup", { mode: "local", setup: true }, "/setup"],
    [
      "oidc",
      { mode: "oidc", setup: false, oidc: { issuer: "id.example" } },
      `/auth/oidc/start?next=${NEXT}`,
    ],
    ["an unknown mode", { mode: "bearer", setup: false }, "reload"],
    ["a mode that is not a string", { mode: 7 }, "reload"],
    ["no details", undefined, "reload"],
    ["null details", null, "reload"],
    ["details that are not an object", "local", "reload"],
    ["setup as a truthy non-boolean", { mode: "local", setup: "yes" }, `/login?next=${NEXT}`],
  ])("%s", (_name, details, want) => {
    expect(run(unauth(details))).toBe(want);
  });

  it("reloads for a 401 that is not dash's answer (an identity proxy in front of dash)", () => {
    expect(run({ status: 401 })).toBe("reload");
  });

  it("ignores details on a 401 that is not dash's answer", () => {
    expect(run({ status: 401, details: { mode: "local", setup: false } })).toBe("reload");
  });

  it.each([
    ["another 401 code", { status: 401, code: "invalid_token" }],
    ["a 403", { status: 403, code: "forbidden" }],
    ["a 403 that says unauthenticated", { status: 403, code: "unauthenticated" }],
    ["a 500", { status: 500, code: "internal" }],
    ["a network error", { status: 0 }],
  ])("does nothing for %s, and leaves the guard free", (_name, err) => {
    expect(run(err)).toBe("none");
    expect(sessionStorage.getItem(REAUTH_KEY)).toBeNull();
    expect(run(unauth({ mode: "proxy", setup: false }))).toBe("reload");
  });
});

describe("handle401: never from a public page", () => {
  it.each(["/login", "/login/", "/login/x", "/setup", "/setup/more"])(
    "does nothing on %s, in every mode, and leaves the guard free",
    (path) => {
      for (const details of [
        undefined,
        { mode: "proxy", setup: false },
        { mode: "local", setup: false },
        { mode: "local", setup: true },
        { mode: "oidc", setup: false },
      ]) {
        expect(run(unauth(details), at(path, "?next=%2F"))).toBe("none");
      }
      expect(run({ status: 401 }, at(path))).toBe("none");
      expect(sessionStorage.getItem(REAUTH_KEY)).toBeNull();
    },
  );

  it.each(["/loginx", "/setups", "/x/login"])("acts on %s, which only looks public", (path) => {
    expect(run(unauth({ mode: "proxy", setup: false }), at(path))).toBe("reload");
  });

  it("isPublicPath matches /login, /setup and below only", () => {
    expect(["/login", "/login/a", "/setup", "/setup/a"].map(isPublicPath)).toEqual([
      true,
      true,
      true,
      true,
    ]);
    expect(["/", "/loginx", "/setupx", "/pods", "/x/login", ""].map(isPublicPath)).toEqual([
      false,
      false,
      false,
      false,
      false,
      false,
    ]);
  });
});

describe("handle401: the 30 s guard", () => {
  const proxy = unauth({ mode: "proxy", setup: false });
  const local = unauth({ mode: "local", setup: false });

  it("acts once per 30 s and records when", () => {
    expect(run(proxy)).toBe("reload");
    expect(sessionStorage.getItem(REAUTH_KEY)).toBe(String(T0));
    vi.setSystemTime(T0 + REAUTH_WINDOW_MS - 1);
    expect(run(proxy)).toBe("none");
    vi.setSystemTime(T0 + REAUTH_WINDOW_MS);
    expect(run(proxy)).toBe("reload");
    expect(sessionStorage.getItem(REAUTH_KEY)).toBe(String(T0 + REAUTH_WINDOW_MS));
  });

  it("is one guard for every mode: a redirect blocks a reload and the other way round", () => {
    expect(run(local)).toBe(`/login?next=${encodeURIComponent("/pods?ns=media")}`);
    expect(run(proxy)).toBe("none");
    expect(run(unauth({ mode: "oidc", setup: false }))).toBe("none");
    expect(run({ status: 401 })).toBe("none");
  });

  it("is shared with the client's existing guard through the same sessionStorage key", () => {
    expect(REAUTH_KEY).toBe("dash:reauth");
    sessionStorage.setItem("dash:reauth", String(T0 - 1_000));
    expect(run(proxy)).toBe("none");
    expect(run(local)).toBe("none");
  });

  it("is not blocked forever by a timestamp in the future (the clock went back)", () => {
    sessionStorage.setItem(REAUTH_KEY, String(T0 + 3_600_000));
    expect(run(proxy)).toBe("reload");
    expect(sessionStorage.getItem(REAUTH_KEY)).toBe(String(T0));
  });

  it("is not blocked by a value that is not a number", () => {
    sessionStorage.setItem(REAUTH_KEY, "garbage");
    expect(run(proxy)).toBe("reload");
  });

  it("fails closed when sessionStorage cannot be read: no reload loop without the guard", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    expect(run(proxy)).toBe("none");
    expect(run(local)).toBe("none");
  });

  it("fails closed when sessionStorage cannot be written", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    expect(run(proxy)).toBe("none");
  });
});

describe("next validation", () => {
  it.each([
    ["//evil", "protocol-relative"],
    ["//evil.example/x", "protocol-relative with a path"],
    ["/\\evil", "backslash after the slash"],
    ["/\\\\evil", "two backslashes"],
    ["/a\\b", "a backslash anywhere"],
    ["https://evil", "absolute URL"],
    ["http://evil.example/pods", "absolute http URL"],
    [`${ORIGIN}/pods`, "absolute URL of this very origin (not a path)"],
    ["javascript:alert(1)", "javascript: scheme"],
    ["JavaScript:alert(1)", "javascript: in another case"],
    ["data:text/html,x", "data: scheme"],
    ["evil", "relative path"],
    ["", "empty"],
    ["/a\nb", "newline"],
    ["/a\rb", "carriage return"],
    ["/a\tb", "tab"],
    ["/\u0000x", "NUL"],
    ["/a\u007fb", "DEL"],
    ["/a\u0085b", "C1 control"],
    ["/login", "the login page (a loop)"],
    ["/login?next=/x", "the login page with a query"],
    ["/setup", "the setup page"],
    ["/a/../login", "a dot segment that resolves to the login page"],
    [`/${"a".repeat(MAX_NEXT_LENGTH)}`, "longer than the limit"],
    // Inputs that pass the raw checks but normalize into something unsafe: the output is checked.
    ["/.//evil.example/x", "a dot segment that normalizes to //evil.example/x"],
    ["/%2e//evil.example/x", "an encoded dot segment that normalizes to //evil.example/x"],
    ["/a/..//evil.example/x", "a parent segment that normalizes to //evil.example/x"],
    ["/%2E%2E//evil.example/x", "an encoded parent segment that normalizes to //evil.example/x"],
    ["/./%09/evil.example", "an encoded tab in the normalized path"],
    ["/%5C/evil.example", "an encoded backslash in the path"],
  ])("refuses %j (%s)", (raw) => {
    expect(validNext(raw, ORIGIN)).toBeUndefined();
  });

  it("never returns something that leaves the origin, for any input of the refused shapes", () => {
    for (const raw of [
      "/.//evil.example/x",
      "/%2e//evil.example/x",
      "/a/..//evil.example/x",
      "/%2E%2E//evil.example/x",
    ]) {
      const out = validNext(raw, ORIGIN);
      expect(out === undefined || new URL(out, ORIGIN).origin === ORIGIN).toBe(true);
      expect(safeNext({ pathname: raw, search: "" }, ORIGIN)).toBe("/");
    }
  });

  it.each([
    ["/", "/"],
    ["/pods", "/pods"],
    ["/pods?ns=media&q=a%20b", "/pods?ns=media&q=a%20b"],
    ["/pods#x", "/pods#x"],
    ["/a/./b/../c", "/a/c"],
    ["/%2F%2Fevil", "/%2F%2Fevil"],
    ["/loginx", "/loginx"],
    ["/./alerts", "/alerts"],
  ])("accepts %j as %j", (raw, want) => {
    expect(validNext(raw, ORIGIN)).toBe(want);
  });

  it("refuses null and undefined", () => {
    expect(validNext(null, ORIGIN)).toBeUndefined();
    expect(validNext(undefined, ORIGIN)).toBeUndefined();
  });

  it("safeNext falls back to / for a path that is not a valid next", () => {
    expect(safeNext(at("//evil.example"), ORIGIN)).toBe("/");
    expect(safeNext(at("/a\\b"), ORIGIN)).toBe("/");
    expect(safeNext(at("/pods", "?ns=a"), ORIGIN)).toBe("/pods?ns=a");
  });

  it("handle401 never sends an unsafe next, and encodes the one it sends", () => {
    const local = unauth({ mode: "local", setup: false });
    expect(run(local, at("//evil.example", "?a=1"))).toBe("/login?next=%2F");
    vi.setSystemTime(T0 + REAUTH_WINDOW_MS);
    expect(run(unauth({ mode: "oidc", setup: false }), at("/a\\b"))).toBe(
      "/auth/oidc/start?next=%2F",
    );
    vi.setSystemTime(T0 + 2 * REAUTH_WINDOW_MS);
    expect(run(local, at("/pods", "?ns=a&x=1"))).toBe(
      `/login?next=${encodeURIComponent("/pods?ns=a&x=1")}`,
    );
  });

  it("handle401 leaves the fragment out of next", () => {
    expect(run(unauth({ mode: "local", setup: false }), at("/pods", "", "#frag"))).toBe(
      "/login?next=%2Fpods",
    );
  });
});
