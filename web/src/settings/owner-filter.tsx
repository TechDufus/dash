import { type FormEvent, type JSX, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/**
 * Whose credentials a list shows: the caller's own (no ?user=), one named user's, or everyone's
 * (?user=*). Only an administrator may ask for anything but their own.
 */
export type Owner = { kind: "own" } | { kind: "user"; name: string } | { kind: "all" };

/** The ?user= value of an owner filter; undefined for the caller's own. */
export function ownerParam(owner: Owner): string | undefined {
  if (owner.kind === "all") return "*";
  if (owner.kind === "user") return owner.name;
  return undefined;
}

type OwnerKind = Owner["kind"];

const OWNERS: { value: OwnerKind; label: string }[] = [
  { value: "own", label: "Mine" },
  { value: "user", label: "One user" },
  { value: "all", label: "All users" },
];

/**
 * The administrator's owner filter: their own, one named user's, or everyone's. `label` names
 * the group ("Whose tokens").
 */
export function OwnerFilter({
  label,
  owner,
  onChange,
}: {
  label: string;
  owner: Owner;
  onChange: (o: Owner) => void;
}): JSX.Element {
  const [kind, setKind] = useState<OwnerKind>(owner.kind);
  const [name, setName] = useState(owner.kind === "user" ? owner.name : "");

  function pick(k: OwnerKind) {
    setKind(k);
    if (k === "own" || k === "all") onChange({ kind: k });
    else onChange({ kind: "user", name: name.trim() });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (name.trim() !== "") onChange({ kind: "user", name: name.trim() });
  }

  return (
    <div className="ml-auto flex flex-wrap items-center gap-2">
      {/* biome-ignore lint/a11y/useSemanticElements: a group of toggle buttons, not a form fieldset */}
      <div role="group" aria-label={label} className="flex flex-wrap gap-1">
        {OWNERS.map((o) => (
          <Button
            key={o.value}
            size="sm"
            variant={o.value === kind ? "secondary" : "ghost"}
            aria-pressed={o.value === kind}
            onClick={() => pick(o.value)}
          >
            {o.label}
          </Button>
        ))}
      </div>
      {kind === "user" ? (
        <form onSubmit={submit} className="flex gap-1">
          <Input
            aria-label="User name"
            autoComplete="off"
            className="h-8 w-40"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" size="sm" variant="outline">
            Show
          </Button>
        </form>
      ) : null}
    </div>
  );
}
