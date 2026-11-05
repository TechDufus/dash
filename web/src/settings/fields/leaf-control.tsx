import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import type { Leaf, Raw } from "../form-model";
import type { ControlProps } from "./field-row";

export interface LeafControlProps {
  leaf: Leaf;
  value: Raw;
  onChange: (raw: Raw) => void;
  control: ControlProps;
}

const SELECT =
  "h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive dark:bg-input/30";

/**
 * The input of one non-secret leaf, by kind: a switch, a select of the enum, a text input (a URL
 * for format: uri), a line list for an array of scalars, a JSON textarea for an array of objects.
 * A locked leaf is read-only (a switch or select, which have no read-only state, is disabled).
 */
export function LeafControl({ leaf, value, onChange, control }: LeafControlProps) {
  const locked = leaf.field?.locked === true;
  const { labelId: _l, errorId: _e, ...a11y } = control;
  switch (leaf.kind) {
    case "boolean":
      return (
        <Switch
          {...a11y}
          checked={value === true}
          disabled={locked}
          onCheckedChange={(checked) => onChange(checked)}
        />
      );
    case "enum": {
      const options = leaf.enum ?? [];
      const current = String(value);
      return (
        <select
          {...a11y}
          className={SELECT}
          value={current}
          disabled={locked}
          onChange={(e) => onChange(e.target.value)}
        >
          {!options.includes(current) && <option value={current}>{current || "(default)"}</option>}
          {options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      );
    }
    case "list":
    case "json":
      return (
        <Textarea
          {...a11y}
          className={cn(leaf.kind === "json" && "font-mono text-xs")}
          value={String(value)}
          readOnly={locked}
          spellCheck={false}
          placeholder={leaf.kind === "list" ? "one per line" : "[]"}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    default:
      return (
        <Input
          {...a11y}
          type={leaf.uri ? "url" : "text"}
          inputMode={
            leaf.kind === "integer" ? "numeric" : leaf.kind === "number" ? "decimal" : undefined
          }
          value={String(value)}
          readOnly={locked}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}
