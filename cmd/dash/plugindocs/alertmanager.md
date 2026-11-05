The Alerts page lists the firing alerts with their labels and the silences that cover them, and
the alerts become attention items on the Overview. With the actions on, an administrator
silences an alert for a set time, and expires a silence dash created.

## Actions

- `silence.create` takes 1 to 20 `{name, value}` matchers, one of them `alertname`; `comment` is
  1 to 500 characters; `duration` is one of `silenceDurations`.
- A silence lasts one of `plugins.alertmanager.silenceDurations` (from `1h`, `4h`, `24h` and `7d`);
  every silence expires.
- Nobody can silence an alert named in `plugins.alertmanager.protectedAlerts` (default
  `Watchdog`), whatever the role: the Alerts page has no Silence button for it and
  `silence.create` answers 403.
- `silence.expire` expires only silences dash created (`createdBy` starting `dash:`).

## Demo twin

Creating a silence mutes the alerts it matches and expiring it unmutes them, so the Alerts page
behaves as it would against a real Alertmanager.
