import type { JSX } from "react";
import {
  type OAuthClient,
  OAuthClientKindBuiltin,
  OAuthClientKindConfig,
  OAuthClientKindMetadata,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";

/** The host of a metadata-document client's ID (its https URL), or the ID when it does not parse. */
export function clientHost(client: OAuthClient): string {
  try {
    return new URL(client.id).host;
  } catch {
    return client.id;
  }
}

/** What a client's kind tells the person: who vouches for it. */
export function clientKindWords(client: OAuthClient): string {
  switch (client.kind) {
    case OAuthClientKindBuiltin:
      return "Built in";
    case OAuthClientKindConfig:
      return "Configured by your admin";
    case OAuthClientKindMetadata:
      return `Unverified · metadata from ${clientHost(client)}`;
    default:
      return "Unverified";
  }
}

/**
 * The badge of a client's kind, on the consent screen and in Connected apps (contract G7-R3
 * fix A4): an app that describes itself through a metadata document is unverified, and the
 * badge names the host that served it, so it cannot pass for a first-party app by its name.
 */
export function ClientKindBadge({ client }: { client: OAuthClient }): JSX.Element {
  const trusted = client.kind === OAuthClientKindBuiltin || client.kind === OAuthClientKindConfig;
  return (
    <Badge
      variant="outline"
      className={trusted ? "text-muted" : "border-warn/40 text-warn"}
      data-kind={client.kind}
    >
      {clientKindWords(client)}
    </Badge>
  );
}
