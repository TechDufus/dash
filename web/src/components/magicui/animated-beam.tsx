"use client"

import { useEffect, useId, useState, type RefObject } from "react"
import { motion } from "motion/react"

import { cn } from "@/lib/utils"

/*
 * dash local change: beams are edge-anchored S-curves instead of magicui's centre-to-centre
 * quadratic. A beam leaves the right edge of `from` and enters the left edge of `to`, so it is
 * drawn only in the gutter between two cards and never across one.
 */

/** The part of a DOMRect the geometry needs (tests pass plain objects; jsdom has no layout). */
export type BeamRect = Pick<DOMRect, "left" | "top" | "width" | "height">

/** A beam's place among the beams that share one card edge (fan-out or fan-in). */
export interface BeamSlot {
  index: number
  count: number
}

export interface BeamPathOptions {
  fromSlot?: BeamSlot
  toSlot?: BeamSlot
}

export interface BeamPoint {
  x: number
  y: number
}

export interface BeamPath {
  /** SVG path data in container coordinates. */
  d: string
  start: BeamPoint
  end: BeamPoint
}

/** Vertical distance between neighbouring endpoints on one card edge, in px. */
const ENDPOINT_GAP = 6
/**
 * Endpoints keep clear of the cards' rounded corners: `rounded-2xl` is `--radius` (0.75rem) times
 * 1.8 in this theme, 21.6 px.
 */
const EDGE_INSET = 22

function slotOffset(rect: BeamRect, slot: BeamSlot | undefined): number {
  if (!slot || slot.count <= 1) return 0
  const room = Math.max(0, rect.height - 2 * EDGE_INSET)
  const step = Math.min(ENDPOINT_GAP, room / (slot.count - 1))
  return (slot.index - (slot.count - 1) / 2) * step
}

/**
 * A cubic bézier from the right edge of `from` to the left edge of `to`, with horizontal
 * tangents at both ends. Both control points sit at the horizontal midpoint, so the curve never
 * leaves the x-range between the two edges. Endpoints that share an edge are spread a few px
 * apart around its vertical centre (less on a short card), so a fan never meets in one point.
 */
export function beamPath(
  from: BeamRect,
  to: BeamRect,
  container: BeamRect,
  { fromSlot, toSlot }: BeamPathOptions = {}
): BeamPath {
  const start = {
    x: from.left + from.width - container.left,
    y: from.top + from.height / 2 - container.top + slotOffset(from, fromSlot),
  }
  const end = {
    x: to.left - container.left,
    y: to.top + to.height / 2 - container.top + slotOffset(to, toSlot),
  }
  const midX = (start.x + end.x) / 2
  return {
    d: `M ${start.x},${start.y} C ${midX},${start.y} ${midX},${end.y} ${end.x},${end.y}`,
    start,
    end,
  }
}

export interface AnimatedBeamProps {
  className?: string
  containerRef: RefObject<HTMLElement | null> // Container ref
  fromRef: RefObject<HTMLElement | null>
  toRef: RefObject<HTMLElement | null>
  /** dash local change: position among the beams leaving `from` / entering `to`. */
  fromSlot?: BeamSlot
  toSlot?: BeamSlot
  reverse?: boolean
  pathColor?: string
  pathWidth?: number
  pathOpacity?: number
  gradientStartColor?: string
  gradientStopColor?: string
  delay?: number
  duration?: number
  repeat?: number
  repeatDelay?: number
  /** dash local change: false renders only the static base line (reduced motion, stopped VMs). */
  animated?: boolean
  /** dash local change: dashed base line. */
  dashed?: boolean
}

export const AnimatedBeam: React.FC<AnimatedBeamProps> = ({
  className,
  containerRef,
  fromRef,
  toRef,
  fromSlot,
  toSlot,
  reverse = false, // Include the reverse prop
  duration = 5,
  delay = 0,
  pathColor = "gray",
  pathWidth = 2,
  pathOpacity = 0.2,
  gradientStartColor = "#ffaa40",
  gradientStopColor = "#9c40ff",
  repeat = Infinity,
  repeatDelay = 0,
  animated = true,
  dashed = false,
}) => {
  const id = useId()
  const [path, setPath] = useState<BeamPath | null>(null)
  const [svgDimensions, setSvgDimensions] = useState({ width: 0, height: 0 })

  const fromIndex = fromSlot?.index
  const fromCount = fromSlot?.count
  const toIndex = toSlot?.index
  const toCount = toSlot?.count

  useEffect(() => {
    const updatePath = () => {
      if (containerRef.current && fromRef.current && toRef.current) {
        const containerRect = containerRef.current.getBoundingClientRect()
        setSvgDimensions({ width: containerRect.width, height: containerRect.height })
        setPath(
          beamPath(
            fromRef.current.getBoundingClientRect(),
            toRef.current.getBoundingClientRect(),
            containerRect,
            {
              fromSlot:
                fromIndex === undefined || fromCount === undefined
                  ? undefined
                  : { index: fromIndex, count: fromCount },
              toSlot:
                toIndex === undefined || toCount === undefined
                  ? undefined
                  : { index: toIndex, count: toCount },
            }
          )
        )
      }
    }

    // Initialize ResizeObserver
    const resizeObserver = new ResizeObserver(() => {
      updatePath()
    })

    // Observe the container element and both endpoints (cards change height with data)
    if (containerRef.current) {
      resizeObserver.observe(containerRef.current)
    }
    if (fromRef.current) resizeObserver.observe(fromRef.current)
    if (toRef.current) resizeObserver.observe(toRef.current)

    // Call the updatePath initially to set the initial path
    updatePath()

    // Clean up the observer on component unmount
    return () => {
      resizeObserver.disconnect()
    }
  }, [containerRef, fromRef, toRef, fromIndex, fromCount, toIndex, toCount])

  // dash local change: the pulse is a band in user space that travels the beam's own x-range
  // (magicui sweeps the whole container, which flashes past a beam that spans only a gutter).
  // The band's head is gradient offset 0 (x1); it enters at one end and leaves past the other.
  const startX = path?.start.x ?? 0
  const endX = path?.end.x ?? 0
  const band = Math.max(16, Math.abs(endX - startX) * 0.75)
  const head = reverse ? [endX, startX - band] : [startX, endX + band]
  const tail = reverse ? [endX + band, startX] : [startX - band, endX]

  return (
    <svg
      data-slot="beam"
      fill="none"
      width={svgDimensions.width}
      height={svgDimensions.height}
      xmlns="http://www.w3.org/2000/svg"
      className={cn(
        "pointer-events-none absolute top-0 left-0 transform-gpu stroke-2",
        className
      )}
      viewBox={`0 0 ${svgDimensions.width} ${svgDimensions.height}`}
      aria-hidden="true"
    >
      <path
        d={path?.d ?? ""}
        stroke={pathColor}
        strokeWidth={pathWidth}
        strokeOpacity={pathOpacity}
        strokeLinecap="round"
        strokeDasharray={dashed ? "4 6" : undefined}
      />
      {animated && path && (
        <>
          <path
            d={path.d}
            strokeWidth={pathWidth}
            stroke={`url(#${id})`}
            strokeOpacity="1"
            strokeLinecap="round"
          />
          <defs>
            <motion.linearGradient
              className="transform-gpu"
              id={id}
              gradientUnits={"userSpaceOnUse"}
              initial={{ x1: head[0], x2: tail[0], y1: 0, y2: 0 }}
              animate={{ x1: head, x2: tail, y1: 0, y2: 0 }}
              transition={{
                delay,
                duration,
                ease: "easeInOut",
                repeat,
                repeatDelay,
              }}
            >
              <stop stopColor={gradientStartColor} stopOpacity="0"></stop>
              <stop stopColor={gradientStartColor}></stop>
              <stop offset="32.5%" stopColor={gradientStopColor}></stop>
              <stop
                offset="100%"
                stopColor={gradientStopColor}
                stopOpacity="0"
              ></stop>
            </motion.linearGradient>
          </defs>
        </>
      )}
    </svg>
  )
}
