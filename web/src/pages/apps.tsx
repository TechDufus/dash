import { Search } from "lucide-react";
import { NoMatches, useLauncherStatus } from "@/components/launcher";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel } from "@/components/panel";
import { Input } from "@/components/ui/input";
import { useQueryParam } from "@/lib/query-param";
import { Slot } from "@/plugins/slots";
import { usePluginsState, usePluginVisible } from "@/plugins/state";

/** The search box and the sections plugins contribute; each section announces its own count. */
export function AppsPage() {
  const [query, setQuery] = useQueryParam("q");
  const status = useLauncherStatus();
  const { byId } = usePluginsState();
  const launcherVisible = usePluginVisible("launcher") === true;
  return (
    <Page>
      <Reveal index={0} className="flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold text-fg">Apps</h1>
        <div className="relative w-full max-w-sm md:ml-auto">
          <label htmlFor="apps-search" className="sr-only">
            Search apps
          </label>
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted"
            aria-hidden="true"
          />
          <Input
            id="apps-search"
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search name, description, group, hostname"
            autoComplete="off"
            className="pl-8"
          />
        </div>
      </Reveal>
      <Reveal index={1}>
        {status === "off" ? (
          <Panel>
            <Empty>
              Nothing to launch: the kubernetes plugin (the apps) is{" "}
              {byId.get("kubernetes")?.state ?? "not available"}, and the launcher plugin (the
              infrastructure links){" "}
              {launcherVisible
                ? "lists no links"
                : `is ${byId.get("launcher")?.state ?? "not available"}`}
              .
            </Empty>
          </Panel>
        ) : (
          <div className="space-y-5">
            <Slot id="apps.sources" query={query} />
          </div>
        )}
        <NoMatches query={query} />
      </Reveal>
    </Page>
  );
}
