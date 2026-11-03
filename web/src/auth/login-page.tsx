import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useSearch } from "wouter";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AuthAlert, AuthCard, Field } from "./auth-card";
import { browserNav, validNext } from "./handle401";
import { AuthRequestError, formatWait, login, probe } from "./session";

/**
 * The refusals /auth/callback sends back as ?error=; any other value is ignored, never echoed. A Map,
 * not an object: the key comes from the URL, and "toString" or "__proto__" must not find anything.
 */
const CALLBACK_ERRORS = new Map<string, string>([
  [
    "not_allowed",
    "Your account is not allowed to sign in to dash. Ask the administrator for access.",
  ],
  ["oidc_failed", "Signing in with the identity provider failed. Try again."],
]);

/** The fixed text of a failed POST /login; the server's message is never shown. */
export function loginErrorText(err: unknown): string {
  const status = err instanceof AuthRequestError ? err.status : -1;
  switch (status) {
    case 401:
      return "Invalid username or password.";
    case 429: {
      const wait = (err as AuthRequestError).retryAfter;
      return wait === undefined
        ? "Too many attempts. Try again later."
        : `Too many attempts. Try again in ${formatWait(wait)}.`;
    }
    case 400:
      return "Enter a username and a password.";
    case 0:
      return "dash is unreachable. Check the connection and try again.";
    default:
      return "Signing in failed. Reload the page and try again.";
  }
}

/**
 * /login: the local account's form, the OIDC button when the server names an identity provider,
 * and the callback's refusals. On success the cached /me is dropped and the browser loads `next`
 * (validated: a same-origin path, "/" otherwise) as a full navigation, so the app starts fresh.
 */
export function LoginPage() {
  const params = new URLSearchParams(useSearch());
  const next = validNext(params.get("next")) ?? "/";
  const callbackError = CALLBACK_ERRORS.get(params.get("error") ?? "");
  const queryClient = useQueryClient();
  const probed = useQuery({
    queryKey: ["auth", "probe"],
    queryFn: probe,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const details = probed.data && "details" in probed.data ? probed.data.details : undefined;
  const mode = probed.data && "me" in probed.data ? probed.data.me.authMode : details?.mode;

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      await login(username, password);
    } catch (err) {
      setPassword("");
      setError(loginErrorText(err));
      setPending(false);
      return;
    }
    void queryClient.invalidateQueries({ queryKey: ["me"] });
    browserNav.assign(next);
  }

  return (
    <AuthCard title="Sign in to dash">
      {callbackError ? <AuthAlert>{callbackError}</AuthAlert> : null}
      {details?.oidc ? (
        <div className="space-y-2">
          <Button asChild className="w-full">
            <a href={`/auth/oidc/start?next=${encodeURIComponent(next)}`}>OIDC sign-in</a>
          </Button>
          <p className="text-center text-xs text-muted">
            Identity provider: <span className="text-fg">{details.oidc.issuer}</span>
          </p>
        </div>
      ) : null}
      {mode === "proxy" ? (
        <p className="text-sm text-muted">
          This dash signs you in through its identity proxy.{" "}
          <a className="text-accent underline-offset-4 hover:underline" href={next}>
            Continue
          </a>
        </p>
      ) : null}
      {mode !== "oidc" && mode !== "proxy" ? (
        <form className="space-y-4" onSubmit={submit}>
          <Field label="Username">
            {(id) => (
              <Input
                id={id}
                name="username"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            )}
          </Field>
          <Field label="Password">
            {(id) => (
              <Input
                id={id}
                name="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            )}
          </Field>
          {error ? <AuthAlert>{error}</AuthAlert> : null}
          <Button type="submit" className="w-full" disabled={pending}>
            {pending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      ) : null}
    </AuthCard>
  );
}
