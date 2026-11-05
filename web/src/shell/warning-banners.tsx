import { Info, X } from "lucide-react";
import { useState } from "react";
import { useMe } from "@/api/queries";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";

/** The me.warnings an administrator sees as a dismissible notice, and what each one says. */
const NOTICES: Record<string, string> = {
  anonymous_viewer:
    "Anonymous viewing is on (auth.local.anonymousViewer): anyone who can reach dash can read it without signing in.",
  oidc_open:
    "OIDC sign-in admits every account the provider authenticates: set auth.oidc.allowedUsers or auth.oidc.allowedGroups to limit it.",
};

/** The sessionStorage key that records a dismissed warning for the rest of the browser session. */
export const dismissKey = (warning: string) => `dash:warning-dismissed:${warning}`;

function readDismissed(warning: string): boolean {
  try {
    return sessionStorage.getItem(dismissKey(warning)) === "1";
  } catch {
    return false;
  }
}

function WarningBanner({ warning, text }: { warning: string; text: string }) {
  const [dismissed, setDismissed] = useState(() => readDismissed(warning));
  if (dismissed) return null;
  const dismiss = () => {
    try {
      sessionStorage.setItem(dismissKey(warning), "1");
    } catch {
      // Storage refused: the notice stays hidden until this page reloads.
    }
    setDismissed(true);
  };
  return (
    <Alert role="status" className="mx-4 mt-3 w-auto pr-12 md:mx-6">
      <Info aria-hidden="true" />
      <AlertDescription>
        <p>{text}</p>
      </AlertDescription>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="Dismiss"
        className="absolute top-2 right-2"
        onClick={dismiss}
      >
        <X aria-hidden="true" />
      </Button>
    </Alert>
  );
}

/**
 * The administrator-only warnings of /me (anonymous_viewer, oidc_open), each a status notice the
 * administrator may dismiss for the rest of the browser session. Viewers see none.
 */
export function WarningBanners() {
  const me = useMe().data;
  if (me?.role !== "admin") return null;
  const warnings = (me.warnings ?? []).filter((w) => Object.hasOwn(NOTICES, w));
  if (warnings.length === 0) return null;
  return (
    <>
      {warnings.map((w) => (
        <WarningBanner key={w} warning={w} text={NOTICES[w] ?? ""} />
      ))}
    </>
  );
}
