The launcher is a list of links to your infrastructure, shown as the Infrastructure section of the
Apps page. The links come from a YAML or JSON file
(`plugins.launcher.file`, see [`docs/launcher.example.yaml`](../launcher.example.yaml)) or are
listed inline (`plugins.launcher.links`).

## Settings

- Mount a file from a ConfigMap and point `plugins.launcher.file` at it, or list the links in
  `plugins.launcher.links`.
- An unreadable file makes the launcher plugin report `error`, and dash still starts.
- An invalid link in `plugins.launcher.links` is refused when the configuration loads, and so is
  an inline `icon` outside the three forms (another origin than `ui.iconBaseURL`, `javascript:`,
  `data:`), named by its entry. Such an icon in the launcher file makes the launcher report
  `error` with a warning that names the entry, and dash still starts.
- A link's `href` that is not `http:` or `https:` is shown as text, not a link.
