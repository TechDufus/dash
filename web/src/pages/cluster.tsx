import { useRef } from "react";
import { useRoute } from "wouter";
import { useTopic } from "@/api/stream";
import { Page, Reveal } from "@/components/page";
import { SourceChips } from "@/components/panel";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useQueryParam } from "@/lib/query-param";
import { EventsTab } from "./cluster/events";
import { NamespacesTab } from "./cluster/namespaces";
import { NodesTab } from "./cluster/nodes";
import { PodsTab } from "./cluster/pods";
import { WorkloadsTab } from "./cluster/workloads";
import { PodSheet } from "./pod-sheet";

const TABS = [
  { value: "nodes", label: "Nodes" },
  { value: "workloads", label: "Workloads" },
  { value: "pods", label: "Pods" },
  { value: "events", label: "Events" },
  { value: "namespaces", label: "Namespaces" },
] as const;

type TabValue = (typeof TABS)[number]["value"];

function isTab(v: string): v is TabValue {
  return TABS.some((t) => t.value === v);
}

interface SheetTarget {
  ns: string;
  name: string;
}

export function ClusterPage() {
  const env = useTopic("cluster");
  const data = env?.data;
  const [rawTab, setTab] = useQueryParam("tab", "nodes");
  const [sheetOpen, sheetParams] = useRoute("/cluster/pods/:ns/:name");

  // Keep the last target while the sheet slides out, so it does not blank mid-animation.
  const lastSheet = useRef<SheetTarget | null>(null);
  if (sheetOpen && sheetParams) lastSheet.current = { ns: sheetParams.ns, name: sheetParams.name };

  // The sheet is opened from the Pods tab, so that tab stays underneath it.
  const tab: TabValue = sheetOpen ? "pods" : isTab(rawTab) ? rawTab : "nodes";

  const nodesReady = data?.nodes.filter((n) => n.ready).length ?? 0;
  const podsRunning = data?.pods.filter((p) => p.phase === "Running").length ?? 0;
  const notReady = data?.workloads.filter((w) => w.health !== "ok").length ?? 0;
  const counts: Record<TabValue, number | undefined> = {
    nodes: data?.nodes.length,
    workloads: data?.workloads.length,
    pods: data?.pods.length,
    events: data?.events.length,
    namespaces: data?.namespaces.length,
  };

  return (
    <Page>
      <Reveal index={0}>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <h1 className="text-xl font-semibold tracking-tight text-fg">Cluster</h1>
          <SourceChips sources={env?.sources} />
          {data && (
            <div className="num ml-auto flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
              <span>
                nodes{" "}
                <span className="text-fg">
                  {nodesReady}/{data.nodes.length}
                </span>
              </span>
              <span>
                namespaces <span className="text-fg">{data.namespaces.length}</span>
              </span>
              <span>
                pods{" "}
                <span className="text-fg">
                  {podsRunning}/{data.pods.length}
                </span>{" "}
                running
              </span>
              <span>
                workloads not ready{" "}
                <span className={notReady > 0 ? "text-warn" : "text-fg"}>{notReady}</span>
              </span>
            </div>
          )}
        </div>
      </Reveal>

      <Reveal index={1}>
        <Tabs value={tab} onValueChange={setTab} className="gap-4">
          <div className="overflow-x-auto pb-2">
            <TabsList variant="line">
              {TABS.map((t) => (
                <TabsTrigger key={t.value} value={t.value} className="flex-none px-3">
                  {t.label}
                  {counts[t.value] !== undefined && (
                    <span className="num text-xs text-muted">{counts[t.value]}</span>
                  )}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
          <TabsContent value="nodes">
            <Reveal>
              <NodesTab data={data} />
            </Reveal>
          </TabsContent>
          <TabsContent value="workloads">
            <Reveal>
              <WorkloadsTab data={data} sources={env?.sources} />
            </Reveal>
          </TabsContent>
          <TabsContent value="pods">
            <Reveal>
              <PodsTab data={data} sources={env?.sources} />
            </Reveal>
          </TabsContent>
          <TabsContent value="events">
            <Reveal>
              <EventsTab data={data} sources={env?.sources} />
            </Reveal>
          </TabsContent>
          <TabsContent value="namespaces">
            <Reveal>
              <NamespacesTab data={data} />
            </Reveal>
          </TabsContent>
        </Tabs>
      </Reveal>

      {lastSheet.current && (
        <PodSheet ns={lastSheet.current.ns} name={lastSheet.current.name} open={sheetOpen} />
      )}
    </Page>
  );
}
