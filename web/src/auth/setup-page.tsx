import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useLayoutEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AuthAlert, AuthCard, Field } from "./auth-card";
import { browserNav } from "./handle401";
import {
  AuthRequestError,
  formatWait,
  MAX_PASSWORD_BYTES,
  MIN_PASSWORD_BYTES,
  passwordBytes,
  setup,
  setupTokenOf,
  stripFragment,
} from "./session";

/** The fixed text of a failed POST /setup; the server's message is never shown. */
export function setupErrorText(err: unknown): string {
  const status = err instanceof AuthRequestError ? err.status : -1;
  switch (status) {
    case 401:
      return "This setup link has expired or is not valid: a link works once, within an hour of the start that printed it. Restart dash to print a new setup link in its log.";
    case 400:
      return `The password must be ${MIN_PASSWORD_BYTES} to ${MAX_PASSWORD_BYTES} bytes long.`;
    case 404:
      return "The administrator account exists already. Sign in instead.";
    case 429: {
      const wait = (err as AuthRequestError).retryAfter;
      return wait === undefined
        ? "Too many attempts. Try again later."
        : `Too many attempts. Try again in ${formatWait(wait)}.`;
    }
    case 0:
      return "dash is unreachable. Check the connection and try again.";
    default:
      return "Setup failed. Reload the page and try again.";
  }
}

/** The client-side check of the two fields; null when they may be sent. */
export function passwordProblem(password: string, confirm: string): string | null {
  const n = passwordBytes(password);
  if (n < MIN_PASSWORD_BYTES) return `Use at least ${MIN_PASSWORD_BYTES} bytes.`;
  if (n > MAX_PASSWORD_BYTES) return `Use at most ${MAX_PASSWORD_BYTES} bytes.`;
  if (password !== confirm) return "The two passwords differ.";
  return null;
}

/**
 * /setup#token=…: sets the administrator's password with the one-time token dash printed at
 * startup. The token is taken from the fragment and the fragment is removed from the address bar
 * before the first paint; the token then lives only in this component's state and the POST body.
 * Reading (the state initializer) and stripping (a layout effect) are separate, so a second run of
 * either (StrictMode) cannot lose the token: the initializer runs before any strip.
 */
export function SetupPage() {
  const queryClient = useQueryClient();
  const [token] = useState(() => setupTokenOf(window.location.hash));
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useLayoutEffect(stripFragment, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (pending || !token) return;
    const problem = passwordProblem(password, confirm);
    if (problem) {
      setError(problem);
      return;
    }
    setPending(true);
    setError(null);
    try {
      await setup(token, password);
    } catch (err) {
      // React copies a controlled input's value into its value attribute: clear both, so the
      // refused password is not left in the page's HTML (as the login page does).
      setPassword("");
      setConfirm("");
      setError(setupErrorText(err));
      setPending(false);
      return;
    }
    void queryClient.invalidateQueries({ queryKey: ["me"] });
    browserNav.assign("/");
  }

  if (token === null) {
    return (
      <AuthCard title="Set up dash">
        <AuthAlert>
          This page needs the setup link dash printed at startup (it ends in #token=…). Open that
          link, or restart dash to print a new one.
        </AuthAlert>
        <a className="text-sm text-accent underline-offset-4 hover:underline" href="/login">
          Sign in instead
        </a>
      </AuthCard>
    );
  }
  return (
    <AuthCard
      title="Set up dash"
      description={`Choose the administrator's password: ${MIN_PASSWORD_BYTES} to ${MAX_PASSWORD_BYTES} bytes.`}
    >
      <form className="space-y-4" onSubmit={submit}>
        <Field label="New password">
          {(id) => (
            <Input
              id={id}
              name="password"
              type="password"
              autoComplete="new-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        <Field label="Repeat the password">
          {(id) => (
            <Input
              id={id}
              name="confirm"
              type="password"
              autoComplete="new-password"
              required
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          )}
        </Field>
        {error ? <AuthAlert>{error}</AuthAlert> : null}
        <Button type="submit" className="w-full" disabled={pending}>
          {pending ? "Setting up…" : "Set password and sign in"}
        </Button>
      </form>
    </AuthCard>
  );
}
