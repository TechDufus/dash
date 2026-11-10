import { lazy } from "react";
import type { HostsData } from "@/api/types";
import { definePlugin, type RouteProps, topic } from "../../types";
import type { MachinesData } from "./model.gen";

const HardwarePage = lazy(() =>
  import("./hardware-page").then((m) => ({
    default: ({ params }: RouteProps) => <m.HardwarePage host={params.host} />,
  })),
);

export default definePlugin({
  id: "hardware",
  order: 40,
  topics: {
    "hardware.hosts": topic<HostsData>(),
    "hardware.machines": topic<MachinesData>(),
  },
  routes: [{ path: "/hardware/:host?", page: HardwarePage }],
});
