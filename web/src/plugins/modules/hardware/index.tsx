import { lazy } from "react";
import type { HostData } from "@/api/types";
import { definePlugin, topic } from "../../types";

const HardwarePage = lazy(() =>
  import("@/pages/hardware").then((m) => ({ default: () => <m.HardwarePage /> })),
);

export default definePlugin({
  id: "hardware",
  order: 40,
  topics: { host: topic<HostData>() },
  routes: [{ path: "/hardware", page: HardwarePage }],
});
