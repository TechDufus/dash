import {
  Activity,
  BellRing,
  Boxes,
  Cloud,
  Container,
  Cpu,
  Database,
  Gauge,
  GitBranch,
  Globe,
  HardDrive,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  type LucideIcon,
  Network,
  Package,
  Puzzle,
  Server,
  Shield,
  Terminal,
  Zap,
} from "lucide-react";

// The lucide icons a plugin can name in NavItem.Icon / UISpec.Icon. A name is a string on the
// wire, so the icons the SPA can draw are listed here; importing the whole lucide namespace would
// put every icon in the entry chunk.
const ICONS: Readonly<Record<string, LucideIcon>> = {
  Activity,
  BellRing,
  Boxes,
  Cloud,
  Container,
  Cpu,
  Database,
  Gauge,
  GitBranch,
  Globe,
  HardDrive,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  Network,
  Package,
  Puzzle,
  Server,
  Shield,
  Terminal,
  Zap,
};

/** The icon for a lucide name; an unknown or empty name gets Puzzle. */
export function iconFor(name: string | undefined): LucideIcon {
  return (name && ICONS[name]) || Puzzle;
}
