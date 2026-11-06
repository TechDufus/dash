import type { ReactNode } from "react";
import { GlobalDialogs } from "@/components/dialogs";
import { Toaster } from "@/components/ui/sonner";
import { AuthBanner } from "./auth-banner";
import { Backdrop } from "./backdrop";
import { CommandPalette } from "./command-palette";
import { PushBanner } from "./push-banner";
import { Rail } from "./rail";
import { RestartBanner } from "./restart-banner";
import { TopBar } from "./topbar";
import { WarningBanners } from "./warning-banners";

export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-dvh">
      <Backdrop />
      <Rail />
      <div className="relative z-10 flex min-h-dvh flex-col md:pl-[72px]">
        <TopBar />
        <AuthBanner />
        <WarningBanners />
        <PushBanner />
        <RestartBanner />
        <main className="flex-1">{children}</main>
      </div>
      <CommandPalette />
      <GlobalDialogs />
      <Toaster position="bottom-right" />
    </div>
  );
}
