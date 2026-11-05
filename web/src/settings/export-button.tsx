import { Download } from "lucide-react";
import { type JSX, useState } from "react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import { Button } from "@/components/ui/button";
import { settingsPaths } from "./api";

/** The name the export is saved under. */
export const EXPORT_FILENAME = "dash-settings.yaml";

/** Saves `body` as a file named `name` through a temporary object URL, revoked once the click
 * has started the download (an immediate revoke can cancel it in Firefox). */
function download(body: string, name: string) {
  const url = URL.createObjectURL(new Blob([body], { type: "text/yaml" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** "Export as config.yaml snippet": downloads GET /settings/export as dash-settings.yaml. */
export function ExportButton(): JSX.Element {
  const [pending, setPending] = useState(false);

  async function exportSettings() {
    setPending(true);
    try {
      download(await api.text(settingsPaths.export), EXPORT_FILENAME);
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setPending(false);
    }
  }

  return (
    <Button variant="outline" size="sm" disabled={pending} onClick={() => void exportSettings()}>
      <Download aria-hidden="true" />
      Export as config.yaml snippet
    </Button>
  );
}
