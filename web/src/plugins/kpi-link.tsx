import type { ReactNode } from "react";
import { Link } from "wouter";

/** A KPI tile that links to the page behind it; the label is what a screen reader hears. */
export function KpiLink({
  href,
  label,
  children,
}: {
  href: string;
  label: string;
  children: ReactNode;
}) {
  return (
    <Link href={href} aria-label={label} className="block rounded-2xl">
      {children}
    </Link>
  );
}

/** Hover treatment shared by the overview tiles. */
export const KPI_TILE = "h-full transition-colors hover:border-accent/30";
