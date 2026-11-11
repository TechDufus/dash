import { Compass } from "lucide-react";
import { Link } from "wouter";
import { settingsAvailable, useMe } from "@/api/queries";
import { Panel } from "@/components/panel";
import { buttonVariants } from "@/components/ui/button";
import { isVisible, usePluginsState } from "@/plugins/state";

/**
 * Where a new administrator starts: shown on the Overview while no plugin that publishes data is
 * visible, so the empty page says what to do. Viewers never see it, nor does demo mode (whose
 * plugins all publish, and which has no settings).
 */
export function SetupHint() {
  const me = useMe().data;
  const { ready, plugins } = usePluginsState();
  if (!ready || !settingsAvailable(me)) return null;
  if (plugins.some((p) => isVisible(p.state) && p.topics.length > 0)) return null;
  return (
    <Panel role="region" aria-labelledby="setup-hint" className="space-y-3 p-4">
      <h2 id="setup-hint" className="flex items-center gap-2 text-sm font-medium text-fg">
        <Compass className="size-4 text-accent" aria-hidden="true" />
        Nothing to show yet
      </h2>
      <p className="text-sm text-muted">
        dash shows what its plugins read: add a Linux machine over SSH to start, or connect the
        services you run in the settings.
      </p>
      <div className="flex flex-wrap gap-2">
        <Link href="/settings/plugins/ssh" className={buttonVariants({ size: "sm" })}>
          Add a host over SSH
        </Link>
        <Link href="/settings" className={buttonVariants({ size: "sm", variant: "outline" })}>
          Open the settings
        </Link>
      </div>
    </Panel>
  );
}
