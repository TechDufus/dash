import { QueryClientProvider } from "@tanstack/react-query";
import { MotionConfig } from "motion/react";
import { queryClient } from "@/api/queries";
import { TooltipProvider } from "@/components/ui/tooltip";
import { cspNonce } from "@/lib/nonce";
import { PluginRoutes } from "@/plugins/routes";
import { Shell } from "@/shell/shell";

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <MotionConfig reducedMotion="user" nonce={cspNonce()}>
        <TooltipProvider delayDuration={150}>
          <Shell>
            <PluginRoutes />
          </Shell>
        </TooltipProvider>
      </MotionConfig>
    </QueryClientProvider>
  );
}
