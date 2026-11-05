import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

function Toggle({ disabled = false }: { disabled?: boolean }) {
  const [on, setOn] = useState(false);
  return (
    <>
      <Label htmlFor="enabled">Enabled</Label>
      <Switch id="enabled" checked={on} onCheckedChange={setOn} disabled={disabled} />
    </>
  );
}

// The settings forms rely on these: a switch named by its label and operable from the keyboard,
// and an alert whose role a banner can lower to status.
describe("form primitives", () => {
  it("a labelled switch toggles with a click and with Space", async () => {
    const user = userEvent.setup();
    render(<Toggle />);
    const sw = screen.getByRole("switch", { name: "Enabled" });
    expect(sw).toHaveAttribute("aria-checked", "false");
    await user.click(screen.getByText("Enabled"));
    expect(sw).toHaveAttribute("aria-checked", "true");
    sw.focus();
    await user.keyboard(" ");
    expect(sw).toHaveAttribute("aria-checked", "false");
  });

  it("a disabled switch does not change", async () => {
    const user = userEvent.setup();
    render(<Toggle disabled />);
    const sw = screen.getByRole("switch", { name: "Enabled" });
    await user.click(sw);
    expect(sw).toHaveAttribute("aria-checked", "false");
  });

  it("an alert is role alert unless told otherwise", () => {
    render(
      <>
        <Alert variant="destructive">
          <AlertTitle>Refused</AlertTitle>
          <AlertDescription>locked by env</AlertDescription>
        </Alert>
        <Alert role="status">
          <AlertDescription>Restart required</AlertDescription>
        </Alert>
      </>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Refusedlocked by env");
    expect(screen.getByRole("status")).toHaveTextContent("Restart required");
  });
});
