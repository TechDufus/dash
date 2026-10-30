import {
  Boxes,
  Container,
  ExternalLink,
  GitMerge,
  Layers,
  Navigation,
  RotateCw,
  Server,
  Zap,
} from "lucide-react";
import { useEffect } from "react";
import { useLocation } from "wouter";
import { reconcileFlux } from "@/api/actions";
import { useTopic } from "@/api/stream";
import { ui, useUi } from "@/api/ui-store";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { NAV } from "./nav";

/** ⌘K / Ctrl+K palette: pages, VMs, nodes, workloads, pods, apps, flux and actions. */
export function CommandPalette() {
  const { palette } = useUi();
  const [, navigate] = useLocation();
  const cluster = useTopic("cluster")?.data;
  const vms = useTopic("vms")?.data;
  const apps = useTopic("apps")?.data;
  const flux = useTopic("flux")?.data;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ui.togglePalette();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const go = (to: string) => {
    ui.setPalette(false);
    navigate(to);
  };

  return (
    <CommandDialog
      open={palette}
      onOpenChange={ui.setPalette}
      title="Command palette"
      description="Jump to pages, machines, workloads and run safe actions"
      className="sm:max-w-xl"
    >
      <Command loop>
        <CommandInput placeholder="Search pages, VMs, workloads, pods, apps…" />
        <CommandList className="max-h-[min(24rem,60vh)]">
          <CommandEmpty>Nothing matches.</CommandEmpty>
          <CommandGroup heading="Pages">
            {NAV.map((n) => (
              <CommandItem
                key={n.to}
                value={`page ${n.label} ${n.title}`}
                onSelect={() => go(n.to)}
              >
                <n.icon aria-hidden="true" />
                {n.label}
              </CommandItem>
            ))}
          </CommandGroup>
          <CommandSeparator />
          <CommandGroup heading="Actions">
            <CommandItem
              value="action reconcile flux-system"
              onSelect={() => {
                ui.setPalette(false);
                void reconcileFlux("GitRepository", "flux-system", "flux-system");
              }}
            >
              <Zap aria-hidden="true" />
              Reconcile flux-system
            </CommandItem>
            {cluster?.workloads
              .filter((w) => w.restartable)
              .map((w) => (
                <CommandItem
                  key={`restart:${w.kind}/${w.namespace}/${w.name}`}
                  value={`action restart ${w.namespace}/${w.name} ${w.kind}`}
                  onSelect={() =>
                    ui.openRestart({
                      kind: w.kind,
                      namespace: w.namespace,
                      name: w.name,
                      protected: w.protected,
                    })
                  }
                >
                  <RotateCw aria-hidden="true" />
                  Restart {w.namespace}/{w.name}
                </CommandItem>
              ))}
          </CommandGroup>
          {vms && vms.vms.length > 0 && (
            <>
              <CommandSeparator />
              <CommandGroup heading="VMs">
                {vms.vms
                  .filter((v) => !v.template)
                  .map((v) => (
                    <CommandItem
                      key={v.vmid}
                      value={`vm ${v.name} ${v.vmid}`}
                      onSelect={() => go(`/vms/${v.vmid}`)}
                    >
                      <Server aria-hidden="true" />
                      {v.name}
                      <span className="num ml-auto text-xs text-faint">{v.vmid}</span>
                    </CommandItem>
                  ))}
              </CommandGroup>
            </>
          )}
          {cluster && (
            <>
              <CommandSeparator />
              <CommandGroup heading="Nodes">
                {cluster.nodes.map((n) => (
                  <CommandItem
                    key={n.name}
                    value={`node ${n.name} ${n.ip}`}
                    onSelect={() => go("/cluster?tab=nodes")}
                  >
                    <Boxes aria-hidden="true" />
                    {n.name}
                    <span className="num ml-auto text-xs text-faint">{n.ip}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
              <CommandSeparator />
              <CommandGroup heading="Workloads">
                {cluster.workloads.map((w) => (
                  <CommandItem
                    key={`${w.kind}/${w.namespace}/${w.name}`}
                    value={`workload ${w.namespace}/${w.name} ${w.kind}`}
                    onSelect={() =>
                      go(
                        `/cluster?tab=workloads&q=${encodeURIComponent(w.name)}&ns=${encodeURIComponent(w.namespace)}`,
                      )
                    }
                  >
                    <Layers aria-hidden="true" />
                    {w.namespace}/{w.name}
                    <span className="ml-auto text-xs text-faint">{w.kind}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
              <CommandSeparator />
              <CommandGroup heading="Pods">
                {cluster.pods.map((p) => (
                  <CommandItem
                    key={`${p.namespace}/${p.name}`}
                    value={`pod ${p.namespace}/${p.name}`}
                    onSelect={() => go(`/cluster/pods/${p.namespace}/${p.name}`)}
                  >
                    <Container aria-hidden="true" />
                    {p.namespace}/{p.name}
                  </CommandItem>
                ))}
              </CommandGroup>
            </>
          )}
          {apps && apps.apps.length > 0 && (
            <>
              <CommandSeparator />
              <CommandGroup heading="Apps">
                {apps.apps.map((a) => (
                  <CommandItem
                    key={a.href}
                    value={`app ${a.name} ${a.href}`}
                    onSelect={() => {
                      ui.setPalette(false);
                      window.open(a.href, "_blank", "noopener,noreferrer");
                    }}
                  >
                    <ExternalLink aria-hidden="true" />
                    {a.name}
                    <span className="ml-auto text-xs text-faint">{a.group}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </>
          )}
          {flux && (
            <>
              <CommandSeparator />
              <CommandGroup heading="Flux">
                {[...flux.kustomizations, ...flux.helmReleases].map((f) => (
                  <CommandItem
                    key={`${f.kind}/${f.namespace}/${f.name}`}
                    value={`flux ${f.kind} ${f.namespace}/${f.name}`}
                    onSelect={() => go(`/gitops?q=${encodeURIComponent(f.name)}`)}
                  >
                    <GitMerge aria-hidden="true" />
                    {f.name}
                    <span className="ml-auto text-xs text-faint">{f.kind}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </>
          )}
          <CommandSeparator />
          <CommandGroup heading="Navigate">
            <CommandItem value="navigate home" onSelect={() => go("/")}>
              <Navigation aria-hidden="true" />
              Back to Overview
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
