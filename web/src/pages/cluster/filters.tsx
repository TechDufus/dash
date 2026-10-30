import { Search } from "lucide-react";
import { useMemo } from "react";
import type { Health } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useQueryParam } from "@/lib/query-param";

export type HealthFilter = "all" | Health;

const HEALTH_OPTIONS: { value: HealthFilter; label: string }[] = [
  { value: "all", label: "All health" },
  { value: "crit", label: "Critical" },
  { value: "warn", label: "Warning" },
  { value: "ok", label: "OK" },
];

/** Namespace, search and health filters, all kept in the URL (?ns=&q=&health=). */
export interface ClusterFilters {
  ns: string;
  setNs: (v: string) => void;
  q: string;
  setQ: (v: string) => void;
  /** Trimmed, lowercased search text for matching. */
  needle: string;
  health: HealthFilter;
  setHealth: (v: string) => void;
}

export function useClusterFilters(): ClusterFilters {
  const [ns, setNs] = useQueryParam("ns", "all");
  const [q, setQ] = useQueryParam("q");
  const [health, setHealth] = useQueryParam("health", "all");
  const needle = useMemo(() => q.trim().toLowerCase(), [q]);
  return { ns, setNs, q, setQ, needle, health: health as HealthFilter, setHealth };
}

export function SearchBox({
  value,
  onChange,
  placeholder,
  label,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  label: string;
}) {
  return (
    <div className="relative min-w-0 flex-1 sm:max-w-xs">
      <Search
        className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted"
        aria-hidden="true"
      />
      <Input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
        className="pl-8"
      />
    </div>
  );
}

interface FilterBarProps {
  filters: ClusterFilters;
  namespaces: string[];
  placeholder: string;
  shown: number;
  total: number;
}

/** Namespace Select + search + health Select, with an "N of M" counter. */
export function FilterBar({ filters, namespaces, placeholder, shown, total }: FilterBarProps) {
  const { ns, setNs, q, setQ, health, setHealth } = filters;
  const options = namespaces.includes(ns) || ns === "all" ? namespaces : [...namespaces, ns];
  return (
    <div className="flex flex-wrap items-center gap-2 px-4 pb-3">
      <Select value={ns} onValueChange={setNs}>
        <SelectTrigger className="w-44" aria-label="Namespace filter">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" className="max-h-80">
          <SelectItem value="all">All namespaces</SelectItem>
          {options.map((n) => (
            <SelectItem key={n} value={n}>
              {n}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <SearchBox value={q} onChange={setQ} placeholder={placeholder} label="Search" />
      <Select value={health} onValueChange={setHealth}>
        <SelectTrigger className="w-36" aria-label="Health filter">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper">
          {HEALTH_OPTIONS.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <span className="num ml-auto text-xs text-muted">
        {shown} of {total}
      </span>
    </div>
  );
}

/** "Show all N" footer for capped tables. */
export function ShowAll({
  shown,
  total,
  onAll,
}: {
  shown: number;
  total: number;
  onAll: () => void;
}) {
  if (shown >= total) return null;
  return (
    <div className="flex items-center justify-center gap-3 border-t border-line px-4 py-3 text-xs text-muted">
      <span className="num">
        Showing {shown} of {total}
      </span>
      <Button type="button" variant="outline" size="xs" onClick={onAll}>
        Show all
      </Button>
    </div>
  );
}
