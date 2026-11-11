# Changelog

The release notes of dash, newest first. CI publishes the section of a version as its GitHub
release when the tag is pushed (docs/development.md, Release): the `## vX.Y.Z: headline` heading
is the title of the release and the text below it, up to the next version, its notes. Notes for
work that is not released yet can wait under a `## Unreleased` heading, which nothing publishes.

The releases before v0.16.1 have their notes on
[GitHub](https://github.com/TechDufus/dash/releases) only.

## v0.16.1: usage figures stay in their cards

The Hardware page's memory and root-disk figures stay inside their cards, and dash is built with Go 1.27.4 for a `net/http` security fix. A patch; nothing to change.

### Fixed

- **Hardware fleet cards:** in a narrow card, a machine's "used / total %" memory and root-disk figures ran past the card's edge. They now wrap (the percentage first, then the total) and never split a number from its unit. The same holds for every usage bar in dash.

### Security

- **Go 1.27.4:** fixes [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859), an HTTP/1 server crash from a header race in `net/http`'s `MaxBytesReader`, which dash's server and clients reach.

### Upgrade notes

- **No step needed.** Rolling back to v0.16.0 is safe, but brings the vulnerable Go back.

