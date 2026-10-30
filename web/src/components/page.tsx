import { motion } from "motion/react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Page enter: fade and an 8 px rise over 200 ms. */
export function Page({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.2, ease: "easeOut" }}
      className={cn("mx-auto w-full max-w-[1600px] space-y-4 p-4 md:p-6", className)}
    >
      {children}
    </motion.div>
  );
}

/** Staggers its children by 30 ms on first render only (initial runs once per mount). */
export function Reveal({
  index = 0,
  children,
  className,
}: {
  index?: number;
  children: ReactNode;
  className?: string;
}) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25, delay: Math.min(index, 20) * 0.03, ease: "easeOut" }}
      className={className}
    >
      {children}
    </motion.div>
  );
}

/** A list item that reorders smoothly (Motion layout). */
export function LayoutItem({
  children,
  className,
  layoutId,
}: {
  children: ReactNode;
  className?: string;
  layoutId?: string;
}) {
  return (
    <motion.div
      layout="position"
      layoutId={layoutId}
      transition={{ duration: 0.25, ease: "easeOut" }}
      className={className}
    >
      {children}
    </motion.div>
  );
}
