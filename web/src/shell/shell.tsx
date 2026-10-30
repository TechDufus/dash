import type { ReactNode } from "react";
import { GlobalDialogs } from "@/components/dialogs";
import { Toaster } from "@/components/ui/sonner";
import { Backdrop } from "./backdrop";
import { CommandPalette } from "./command-palette";
import { Rail } from "./rail";
import { TopBar } from "./topbar";

export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-dvh">
      <Backdrop />
      <Rail />
      <div className="relative z-10 flex min-h-dvh flex-col md:pl-[72px]">
        <TopBar />
        <main className="flex-1">{children}</main>
      </div>
      <CommandPalette />
      <GlobalDialogs />
      <Toaster position="bottom-right" />
    </div>
  );
}
