import { Page } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { Slot } from "@/plugins/slots";
import { useTopicAvailable } from "@/plugins/state";

/**
 * The panels come from every plugin that has one (kubernetes, proxmox, cnpg, rustfs), and each
 * draws its own skeleton until its topic arrives. The page only decides whether there is anything
 * to draw: when no plugin lists a storage topic it says so without naming a plugin, so a new
 * storage plugin needs no change here beyond its topic in this list.
 */
export function StoragePage() {
  const kubernetes = useTopicAvailable("kubernetes.storage");
  const proxmox = useTopicAvailable("proxmox.storage");
  const cnpg = useTopicAvailable("cnpg.clusters");
  const rustfs = useTopicAvailable("rustfs.usage");

  // The plugin list has not arrived: nothing can be drawn or ruled out yet.
  if ([kubernetes, proxmox, cnpg, rustfs].includes(undefined)) {
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

  if (!kubernetes && !proxmox && !cnpg && !rustfs) {
    return (
      <Page>
        <Panel>
          <Empty>No storage data: none of the plugins that report storage is running.</Empty>
        </Panel>
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
