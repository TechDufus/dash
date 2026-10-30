import { DotPattern } from "@/components/magicui/dot-pattern";

/** Fixed, non-animated background: two cyan radial glows plus a masked dot grid. */
export function Backdrop() {
  return (
    <div aria-hidden="true" className="pointer-events-none fixed inset-0 z-0 overflow-hidden">
      <div
        className="absolute inset-0"
        style={{
          background:
            "radial-gradient(1200px 600px at 0% 0%, rgb(56 225 255 / 0.10), transparent 60%), radial-gradient(900px 500px at 100% 100%, rgb(56 225 255 / 0.05), transparent 60%)",
        }}
      />
      <DotPattern
        width={22}
        height={22}
        cr={1}
        className="absolute inset-0 text-white/70 opacity-25 [mask-image:radial-gradient(ellipse_at_center,white,transparent_75%)]"
      />
    </div>
  );
}
