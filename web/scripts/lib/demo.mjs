// dash in demo mode on free loopback ports, for the scripts that drive a browser against it
// (screenshots.mjs, pwa-smoke.mjs).
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { constants } from "node:os";

/** Every dash this process started and that has not exited yet; stopAll stops them. */
const running = new Set();

/**
 * n distinct free loopback ports: every listener on port 0 stays open until all n are bound, so
 * the kernel cannot hand the same port out twice, and only then are they all closed.
 */
export async function freePorts(n) {
  const servers = [];
  try {
    for (let i = 0; i < n; i++) {
      const srv = createServer();
      srv.unref();
      servers.push(srv);
      await new Promise((resolve, reject) => {
        srv.once("error", reject);
        srv.listen(0, "127.0.0.1", resolve);
      });
    }
    return servers.map((srv) => srv.address().port);
  } finally {
    await Promise.all(
      servers
        .filter((srv) => srv.listening)
        .map((srv) => new Promise((resolve) => srv.close(resolve))),
    );
  }
}

/**
 * Starts the dash binary bin with DASH_DEMO=true, the API on apiPort and health on healthPort (the
 * public URL is the API's loopback address unless env sets DASH_SERVER_PUBLIC_URL), plus env, and
 * resolves once /readyz answers 200; rejects if it exits first or is not ready within 30 s. Only
 * PATH and HOME come from this process's environment. The child is registered before the wait, so
 * stopAll stops a dash that is still starting.
 */
export async function startDash(bin, apiPort, healthPort, env = {}) {
  const childEnv = {
    PATH: process.env.PATH ?? "",
    HOME: process.env.HOME ?? "",
    DASH_DEMO: "true",
    DASH_SERVER_LISTEN: `127.0.0.1:${apiPort}`,
    DASH_SERVER_HEALTH_LISTEN: `127.0.0.1:${healthPort}`,
    DASH_SERVER_PUBLIC_URL: `http://127.0.0.1:${apiPort}`,
    ...env,
  };
  const child = spawn(bin, [], { env: childEnv, stdio: ["ignore", "pipe", "pipe"] });
  const exited = new Promise((resolve) => child.once("exit", (code, sig) => resolve(sig ?? code)));
  const dash = { child, exited };
  running.add(dash);
  exited.then(() => running.delete(dash));
  let log = "";
  child.stdout.on("data", (b) => {
    log += b;
  });
  child.stderr.on("data", (b) => {
    log += b;
  });
  const deadline = Date.now() + 30_000;
  for (;;) {
    const status = await Promise.race([
      fetch(`http://127.0.0.1:${healthPort}/readyz`).then(
        (r) => r.status,
        () => 0,
      ),
      exited.then((how) => `exit ${how}`),
    ]);
    if (status === 200) {
      return { ...dash, log: () => log };
    }
    if (typeof status === "string" || Date.now() > deadline) {
      child.kill("SIGKILL");
      throw new Error(`dash did not become ready (${status}):\n${log}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** Stops every dash startDash started, ready or not: SIGTERM, then SIGKILL after 10 s. */
export async function stopAll() {
  await Promise.all(
    [...running].map(async ({ child, exited }) => {
      child.kill("SIGTERM");
      const timer = setTimeout(() => child.kill("SIGKILL"), 10_000);
      await exited;
      clearTimeout(timer);
    }),
  );
}

/** 128 plus the number of the signal onSignals caught, once one arrived. */
let signalled;

/**
 * Runs stop once on SIGINT, SIGTERM or SIGHUP and exits with 128 plus the signal's number, as a
 * shell reports a process the signal ended.
 */
export function onSignals(stop) {
  for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.once(sig, () => {
      signalled = 128 + constants.signals[sig];
      stop().finally(() => process.exit(signalled));
    });
  }
}

/**
 * Exits with code, or with the signal's code when a signal arrived: stopping dash under a run makes
 * the run fail, and that failure must not report itself as the reason the process ended.
 */
export function exit(code) {
  process.exit(signalled ?? code);
}
