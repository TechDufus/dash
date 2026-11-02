import { useTopic } from "@/api/stream";
import { Page } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { Slot } from "@/plugins/slots";
import { usePluginsState, useTopicAvailable } from "@/plugins/state";

/**
 * The panels come from every plugin that has one, and each reads its own topic. The page reads
 * kubernetes.storage only to decide between its skeleton and its panels; the Proxmox panel reads
 * proxmox.storage itself, so the page is usable with either plugin alone.
 */
export function StoragePage() {
  const env = useTopic("kubernetes.storage");
  const available = useTopicAvailable("kubernetes.storage");
  const proxmoxAvailable = useTopicAvailable("proxmox.storage");
  const { byId } = usePluginsState();

  if (available === false && proxmoxAvailable === false) {
    const kube = byId.get("kubernetes")?.state ?? "not available";
    const pve = byId.get("proxmox")?.state ?? "not available";
    return (
      <Page>
        <Panel>
          <Empty>
            Storage data comes from the kubernetes plugin, which is {kube}, and the proxmox plugin,
            which is {pve}.
          </Empty>
        </Panel>
      </Page>
    );
  }

  // Only the Proxmox panel can draw: the kubernetes panels read a topic that nobody publishes.
  if (available === false) {
    return (
      <Page>
        <div className="grid gap-4 lg:grid-cols-2">
          <Slot id="storage" />
        </div>
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
        <Slot id="storage" />
      </div>
    </Page>
  );
}
