import { RotateCw } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { useSettingsView } from "@/settings/api";

/**
 * The shell's notice that settings changed which only take effect after a restart, listing their
 * paths. The list is the server's (fresh per process), so it is gone once dash has restarted; it is
 * re-read every 30 s and on window focus. Administrators only: for anyone else the settings view is
 * never requested and nothing is drawn.
 */
export function RestartBanner() {
  const view = useSettingsView({ refetchInterval: 30_000, refetchOnWindowFocus: true });
  const paths = view.data?.restartRequired ?? [];
  if (paths.length === 0) return null;
  return (
    <Alert role="status" className="mx-4 mt-3 w-auto md:mx-6">
      <RotateCw aria-hidden="true" />
      <AlertTitle>Restart required</AlertTitle>
      <AlertDescription>
        <p>These settings take effect after dash restarts:</p>
        <ul className="flex flex-wrap gap-x-3 gap-y-1">
          {paths.map((p) => (
            <li key={p} className="font-mono text-xs text-fg">
              {p}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  );
}
