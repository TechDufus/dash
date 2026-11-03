import { type ReactNode, useId } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

/** The frame of the public pages: one centred card with a title. */
export function AuthCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <main className="grid min-h-dvh place-items-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>
            <h1 className="text-base font-semibold tracking-tight text-fg">{title}</h1>
          </CardTitle>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </CardHeader>
        <CardContent className="space-y-4">{children}</CardContent>
      </Card>
    </main>
  );
}

/** A message the page must announce: a failed sign-in, a refused callback, a bad setup link. */
export function AuthAlert({ children }: { children: ReactNode }) {
  return (
    <p
      role="alert"
      className="rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
    >
      {children}
    </p>
  );
}

/** A labelled input: `children` renders the input with the id the label points at. */
export function Field({ label, children }: { label: string; children: (id: string) => ReactNode }) {
  const id = useId();
  return (
    <div className="space-y-1.5 text-sm">
      <label htmlFor={id} className="block text-muted">
        {label}
      </label>
      {children(id)}
    </div>
  );
}
