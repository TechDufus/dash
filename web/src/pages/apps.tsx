import { Search } from "lucide-react";
import { useTopic } from "@/api/stream";
import { appMatches, infraMatches, Launcher } from "@/components/launcher";
import { Page, Reveal } from "@/components/page";
import { Input } from "@/components/ui/input";
import { INFRA_LINKS } from "@/config/links";
import { useQueryParam } from "@/lib/query-param";

export function AppsPage() {
  const env = useTopic("apps");
  const [query, setQuery] = useQueryParam("q");
  const apps = env?.data.apps;
  const matchedApps = apps?.filter((a) => appMatches(a, query)).length ?? 0;
  const matchedInfra = INFRA_LINKS.filter((l) => infraMatches(l, query)).length;
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
        <p className="num w-full text-xs text-muted" aria-live="polite">
          {apps
            ? `${matchedApps} of ${apps.length} apps · ${matchedInfra} of ${INFRA_LINKS.length} infrastructure links`
            : "Loading apps…"}
        </p>
      </Reveal>
      <Reveal index={1}>
        <Launcher
          apps={apps}
          infra={INFRA_LINKS}
          sources={env?.sources}
          query={query}
          emptyText={`No apps match “${query}”.`}
        />
      </Reveal>
    </Page>
  );
}
