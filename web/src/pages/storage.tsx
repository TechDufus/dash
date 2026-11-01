import { useTopic } from "@/api/stream";
import { Page } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { Slot } from "@/plugins/slots";
import { usePluginsState, useTopicAvailable } from "@/plugins/state";

/** The storage topic comes from the kubernetes plugin; the panels come from every plugin that has one. */
export function StoragePage() {
  const env = useTopic("storage");
  const available = useTopicAvailable("storage");
  const { byId } = usePluginsState();

  if (available === false) {
    const state = byId.get("kubernetes")?.state;
    return (
      <Page>
        <Panel>
          <Empty>
            Storage data comes from the kubernetes plugin, which is {state ?? "not available"}.
          </Empty>
        </Panel>
      </Page>
    );
  }

  if (!env) {
    return (
      <Page>
        <PanelSkeleton lines={6} />
        <div className="grid gap-4 lg:grid-cols-2">
          <PanelSkeleton lines={3} />
          <PanelSkeleton lines={3} />
          <PanelSkeleton lines={3} />
          <PanelSkeleton lines={2} />
        </div>
      </Page>
    );
  }

  return (
    <Page>
      <div className="grid gap-4 lg:grid-cols-2">
        <Slot id="storage" env={env} />
      </div>
    </Page>
  );
}
