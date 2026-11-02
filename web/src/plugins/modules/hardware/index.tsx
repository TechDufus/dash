import { lazy } from "react";
import type { HostData } from "@/api/types";
import { definePlugin, topic } from "../../types";
import type { MachinesData } from "./model.gen";

const HardwarePage = lazy(() =>
  import("./hardware-page").then((m) => ({ default: () => <m.HardwarePage /> })),
);

export default definePlugin({
  id: "hardware",
  order: 40,
  topics: {
    "hardware.hosts": topic<HostData>(),
    "hardware.machines": topic<MachinesData>(),
  },
  routes: [{ path: "/hardware", page: HardwarePage }],
});
