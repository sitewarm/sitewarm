# Changelog

All notable changes to sitewarm are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). The Go packages under `pkg/` are
the public API.

## [Unreleased]

## [1.1.0-rc.1] - 2026-10-27

### Added

- `sitewarm scan` prints a one-line notice on stderr when a newer release
  exists. It is off in CI, when stderr is not a terminal and with
  `SITEWARM_NO_UPDATE_CHECK=1` or `--no-update-check`; it never installs
  anything, never delays a run and caches the answer for 24 hours. See
  "Update notice" in the README for what the request sends.
- With `--from-sitemap`, each entry in the JSON report has an optional
  `lastmod`: the `<lastmod>` of the URL as written in the sitemap. It is left
  out when the sitemap has none or the value is not a W3C datetime, and it
  never changes a state or the verdict. The existing fields are unchanged.
  In Go, `Result` entries have a `LastMod` string.
- Each entry in the JSON report has `canonical` (the canonical URL of the
  page, when it has one) and `warmed` (whether the measurement came after the
  URL had been requested before, or was already a `HIT`). Existing fields are
  unchanged. In Go, `Result` entries have `Canonical` and `Warmed`.

## [1.0.2] - 2026-10-24

### Fixed

- `sitewarm --version` and the `sitewarm/<version>` User-Agent token now show
  the real version for a binary installed with
  `go install github.com/sitewarm/sitewarm/cmd/sitewarm@vX.Y.Z`. They used to
  show `dev`. Release archives are unchanged.

### Security

- The frozen Python code under `legacy/` now pins `certifi` 2026.10.19
  (the previous pin was affected by a compromised-intermediate CA advisory).
  The Go CLI and `pkg/` are not affected.

## [1.0.1] - 2026-10-24

### Changed

- The Go module path is now `github.com/sitewarm/sitewarm` (the repository
  moved from `clarivo-co/sitewarm`). Import
  `github.com/sitewarm/sitewarm/pkg/sitewarm` and install with
  `go install github.com/sitewarm/sitewarm/cmd/sitewarm@latest`. The API of
  `pkg/sitewarm` and the behavior of the CLI are unchanged.

## [1.0.0] - 2026-10-22

First stable release of the Go rewrite. The original Python sitemap crawler
is frozen at tag [`v0-python`](https://github.com/sitewarm/sitewarm/tree/v0-python).

### Added

- `sitewarm scan`: warms your top pages through the CDN after a deploy and
  verifies the cache state per URL and browser profile (desktop and mobile)
  on Cloudflare, Vercel, CloudFront, Fastly and Akamai.
- `--top-from` to read a prioritised CSV of URLs or paths, and
  `--from-sitemap` to read a sitemap (indexes, nested indexes and `.gz`
  included) filtered by `robots.txt`.
- `--format` (table, json, csv) and `--report <file>` for a machine-readable
  report, and `--fail-on` to choose which states fail the run.
- Honest requests: a `sitewarm/<version>` token in the User-Agent, global
  rate limit, bounded concurrency, retries of `miss` and `stale` URLs and a
  run timeout. sitewarm never impersonates a crawler.
- Release archives for Linux, macOS and Windows on amd64 and arm64, with
  `checksums.txt`, built by GoReleaser from a tag.
- `scripts/install.sh`, a checksum-verifying installer for Linux and macOS.
- `scripts/t2.sh`, to measure warming against a control group.
- README sections for installation, CI usage and the JSON report schema.

### Fixed

- A run where every result is `unknown` (for example a site with no CDN) no
  longer says the cache is verified. Results skipped by `robots.txt` are not
  counted as requested.

## [1.0.0-rc.1] - 2026-10-21

First tagged release of the Go rewrite, published as a pre-release.
