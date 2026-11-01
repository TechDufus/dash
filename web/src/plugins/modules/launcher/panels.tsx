import { InfraSection } from "@/components/launcher";
import type { ModulePanels } from "../../types";

export const panels: ModulePanels = {
  // The infrastructure links come from the launcher.links topic.
  "apps.sources": [{ id: "infrastructure", order: 20, component: InfraSection }],
};
