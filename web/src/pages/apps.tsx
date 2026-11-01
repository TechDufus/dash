import { Search } from "lucide-react";
import { NoMatches } from "@/components/launcher";
import { Page, Reveal } from "@/components/page";
import { Input } from "@/components/ui/input";
import { useQueryParam } from "@/lib/query-param";
import { Slot } from "@/plugins/slots";

/** The search box and the sections plugins contribute; each section announces its own count. */
export function AppsPage() {
  const [query, setQuery] = useQueryParam("q");
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
        <div className="space-y-5">
          <Slot id="apps.sources" query={query} />
        </div>
        <NoMatches query={query} />
      </Reveal>
    </Page>
  );
}
