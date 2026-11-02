import { Boxes } from "lucide-react";
import { useTopic } from "@/api/stream";
import { BytesBar } from "@/components/bytes-bar";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { StatusDot } from "@/components/status";
import type { ModulePanels, SlotProps } from "../../types";

/** Reads its own topic and draws a skeleton, not "No buckets.", until it arrives. */
export function ObjectStoragePanel(_: SlotProps["storage"]) {
  const env = useTopic("rustfs.usage");
  if (!env) return <PanelSkeleton lines={3} />;
  const { buckets, capacity } = env.data;
  return (
    <Panel className="h-full">
      <PanelHeader title="Object storage" icon={<Boxes />} />
      <div className="px-4 pb-4">
        <div className="space-y-4">
          {buckets.length === 0 && <Empty className="py-4">No buckets.</Empty>}
          {buckets.map((b) => (
            <div key={b.name} className="flex items-start gap-2">
              <StatusDot health={b.health} className="mt-1" />
              <BytesBar
                used={b.usedBytes}
                total={b.quotaBytes}
                label={b.name}
                noTotalLabel="no quota"
              />
            </div>
          ))}
        </div>
        <div className="mt-5 border-t border-line pt-4">
          <BytesBar
            used={capacity.usedBytes}
            total={capacity.usableBytes}
            label="RustFS drive (usable)"
          />
        </div>
      </div>
    </Panel>
  );
}

export const panels: ModulePanels = {
  storage: [
    {
      id: "object-storage",
      order: 20,
      reads: ["rustfs.usage"],
      component: ObjectStoragePanel,
    },
  ],
};
