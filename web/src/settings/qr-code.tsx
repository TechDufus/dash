import type { JSX } from "react";
import { encode } from "uqr";

/**
 * A QR code of `value` (D47), drawn as SVG <rect>s, one per dark module: no image, no data: URL
 * and no inline style, so the page's CSP needs nothing new. The 4-module quiet zone is part of
 * the drawing, on a white background, so a camera reads it on the dark theme too.
 */
export function QrCode({ value, className }: { value: string; className?: string }): JSX.Element {
  const qr = encode(value, { ecc: "M", border: 4 });
  const dark: JSX.Element[] = [];
  qr.data.forEach((row, y) => {
    row.forEach((on, x) => {
      // biome-ignore lint/suspicious/noArrayIndexKey: a module is its position; the grid never reorders
      if (on) dark.push(<rect key={`${x},${y}`} x={x} y={y} width={1} height={1} />);
    });
  });
  return (
    <svg
      role="img"
      aria-label={`QR code of ${value}`}
      viewBox={`0 0 ${qr.size} ${qr.size}`}
      shapeRendering="crispEdges"
      className={className}
      data-qr-size={qr.size}
    >
      <rect width={qr.size} height={qr.size} fill="#ffffff" />
      <g fill="#000000">{dark}</g>
    </svg>
  );
}
