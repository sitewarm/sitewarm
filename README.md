<h1 align="center">
  <img src="docs/assets/ember.svg" alt="Ember, the sitewarm mascot: a small flame with a green check badge" width="72" valign="middle" /> sitewarm
</h1>

<p align="center">
  <a href="https://github.com/sitewarm/sitewarm/actions/workflows/ci.yml"><img src="https://github.com/sitewarm/sitewarm/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/sitewarm/sitewarm/stargazers"><img src="https://img.shields.io/github/stars/sitewarm/sitewarm?style=flat&amp;label=%E2%98%85&amp;color=F0602A" alt="GitHub stars" /></a>
  <img src="https://img.shields.io/github/go-mod/go-version/sitewarm/sitewarm?style=flat" alt="Go version" />
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-F0602A?style=flat" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Linux%20%7C%20macOS%20%7C%20Windows-amd64%20%7C%20arm64-4493F8?style=flat" alt="Linux, macOS and Windows on amd64 and arm64" />
  <a href="https://github.com/sponsors/ernestova"><img src="https://img.shields.io/badge/sponsor-%E2%9D%A4-EA4AAA?style=flat&amp;logo=githubsponsors&amp;logoColor=white" alt="Sponsor sitewarm" /></a>
</p>

<p align="center">
  <strong>Warm your key pages after a deploy, and see per URL whether the CDN serves them from cache.</strong><br/>
  Per URL and browser profile, on Cloudflare, Vercel, CloudFront, Fastly and Akamai.
</p>

<p align="center">
  <img src="docs/assets/demo.png" alt="sitewarm scan output: six URL and profile pairs on Cloudflare and Vercel, all hit" width="960" />
</p>

> sitewarm is a Go rewrite. The original Python sitemap crawler lives in
> [`legacy/`](legacy/) and is frozen at tag
> [`v0-python`](https://github.com/sitewarm/sitewarm/tree/v0-python).
> See the [changelog](CHANGELOG.md).

## Install

On Linux or macOS, one line installs the latest stable release into
`~/.local/bin` after checking its SHA-256:

```
curl -fsSL https://raw.githubusercontent.com/sitewarm/sitewarm/master/scripts/install.sh | sh
```

Set `SITEWARM_VERSION` to pick a release and `SITEWARM_INSTALL_DIR` to pick the
directory. Read [`scripts/install.sh`](scripts/install.sh) first if you prefer;
it is short.

Or download the archive for your platform from the
[releases page](https://github.com/sitewarm/sitewarm/releases): Linux, macOS
and Windows, on amd64 and arm64. Each release has a `checksums.txt` with the
SHA-256 of every archive. Check the download before you unpack it:

```
VERSION=1.0.2   # the release you want, without the leading v
FILE=sitewarm_${VERSION}_linux_amd64.tar.gz
curl -fsSLO https://github.com/sitewarm/sitewarm/releases/download/v${VERSION}/${FILE}
curl -fsSLO https://github.com/sitewarm/sitewarm/releases/download/v${VERSION}/checksums.txt
grep " ${FILE}\$" checksums.txt | sha256sum -c -     # macOS: shasum -a 256 -c -
tar -xzf "$FILE" sitewarm
./sitewarm --version
```

Archive names are `sitewarm_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows),
with `<os>` one of `linux`, `darwin`, `windows` and `<arch>` one of `amd64`,
`arm64`. On Windows, compare `(Get-FileHash <file> -Algorithm SHA256).Hash` with
the line for that file in `checksums.txt`. Each archive also holds `LICENSE`
and this README.

With Go 1.26 or newer you can install from source instead:

```
go install github.com/sitewarm/sitewarm/cmd/sitewarm@latest
```

A binary built this way reports its version as `dev`; the release archives
report the release version.

### Build from source

```
go build -o sitewarm ./cmd/sitewarm
```

`sitewarm --version` prints the version.

## Usage

```
sitewarm scan --top-from top.csv
```

`top.csv` has a header row with a `url` column (absolute URLs) or a `path`
column (resolved with `--base-url`). Rows are in priority order; `--top N`
keeps the first N (default 50).

Or read the URLs from a sitemap:

```
sitewarm scan --from-sitemap https://example.com/sitemap.xml
```

Sitemap indexes are followed, nested ones included, and gzip-compressed
sitemaps (`.gz`) are read. URLs keep their sitemap order and `--top N` keeps
the first N allowed by `robots.txt` (see below). Pass exactly one of
`--top-from` or `--from-sitemap`.

With `--from-sitemap`, sitemap URLs are checked against the site's
`robots.txt` first: the `sitewarm` group applies, or `*` when there is no
`sitewarm` group. A disallowed URL is not requested and is reported as
`skipped-robots`. Blocked URLs do not count toward `--top`: `--top 50` warms
50 allowed URLs. The blocked URLs met before the 50th allowed one are reported
as `skipped-robots`; reading stops there, so the rest of the sitemap is not
fetched, checked or reported. A
`robots.txt` that returns any 4xx status (401, 403, 404, 410...) allows
everything, as RFC 9309 says; a 5xx status, a timeout or a network or DNS error
skips every URL of that host, with a warning.
`skipped-robots` does not fail the run unless it is in `--fail-on`, but a run
where no URL was verified always fails. `--ignore-robots` turns the check off.
A `--top-from` list is your own choice of pages, so `robots.txt` is not read
for it.

Each URL is requested with two profiles, `desktop` and `mobile`, that send the
navigation headers of current Chrome stable (Windows and Android) plus a
`sitewarm/<version>` token in the User-Agent. sitewarm never impersonates a
crawler. Requests are spread with `--concurrency 4` and a global `--rps 5`
that also covers retries and the sitemap and `robots.txt` fetches. A `miss` or `stale` URL is requested again every
`--retry-interval 10s` until it is a `hit` or `--max-wait 5m` passes. The whole
run, including reading a sitemap and `robots.txt`, stops at
`--run-timeout 15m`.

| Flag | Default | |
|---|---|---|
| `--top-from` | | CSV with the URLs |
| `--from-sitemap` | | sitemap or sitemap index URL |
| `--ignore-robots` | | with `--from-sitemap`, do not check `robots.txt` |
| `--base-url` | | base for a `path` column |
| `--top` | 50 | URLs to warm (allowed URLs, with `--from-sitemap`) |
| `--concurrency` | 4 | requests in flight |
| `--rps` | 5 | global requests per second, sitemap and `robots.txt` included |
| `--retry-interval` | 10s | wait between attempts |
| `--max-wait` | 5m | retry window per URL (`0` = one request) |
| `--run-timeout` | 15m | cap for the whole run |
| `--fail-on` | `miss,client-error,error,incomplete,noindex,redirect-error` | states that fail the run |
| `--accept-language` | `en-US,en;q=0.9` | for both profiles |
| `--cookie-hosts` | | hosts that may receive cookies |
| `--format` | `table` | stdout format: `table`, `json` or `csv` |
| `--report` | | also write the JSON report to a file |

`--report <file>` does not replace the stdout output: the run prints the
`--format` output (the table by default) and also writes the full JSON report
to the file, even when the run fails, is interrupted or times out. It is not
written when the arguments are invalid.

`--format csv` writes the columns `url,status,state`, one row per URL and
profile, so each URL appears once per profile. `status` is the HTTP status
code, empty when there was no response.

### States

| State | Meaning |
|---|---|
| `hit` | served from the edge cache |
| `stale` | served from cache but expired |
| `miss` | not in cache |
| `bypass` | the CDN does not cache it (BYPASS, DYNAMIC, or a miss with `private`/`no-store`) |
| `variant-unverifiable` | the response has `Vary: User-Agent` |
| `challenged` | 403 or a bot challenge |
| `client-error` | another 4xx (404, 410...) |
| `error` | 5xx, timeout or network error, after 3 retries |
| `redirect-error` | redirect loop, more than 5 hops, or a hop to another host |
| `incomplete` | not finished before `--run-timeout` or an interrupt |
| `unknown` | the CDN does not expose a cache status |
| `skipped-robots` | a sitemap URL not requested because of `robots.txt` |

### Failing on `noindex`

`noindex` is not a cache state but a `--fail-on` token, and it is in the
default. A result fails with it when the final response of a successful
request says the page should not be indexed: an `X-Robots-Tag` header, or a
`robots` or `googlebot` meta tag in the first 256 KB of an HTML page, with
`noindex`. A body that cannot be decoded (`indexability-error`) also fails with
`noindex`, because the page cannot be shown to be indexable. The outcome of
the check is the `indexability` field of the JSON report and the `INDEX`
column of the table: `indexable`, `noindex`, `not-html`,
`indexability-inconclusive` (no `</head>` within 256 KB) or
`indexability-error`. `indexability-inconclusive` is also a `--fail-on` token;
it is not in the default.

Supported CDNs: Cloudflare (`cf-cache-status`), Vercel (`x-vercel-cache`),
CloudFront, Fastly (the last value of `x-cache`, the edge that answered) and
Akamai (debug headers requested with `Pragma`). Unmapped values are `unknown`
and the raw value is kept. When several CDNs are stacked, the outermost one
decides.

Redirects are followed up to 5 hops on the same host (apex and `www` count as
one) and the final URL is verified. Bodies are decoded (gzip, deflate, br,
zstd) and the first 256 KB of HTML are checked for `noindex`; the rest is
downloaded and discarded so the CDN finishes caching it. Each request has a
30 s deadline.

### Cookies

Cookies come only from `SITEWARM_COOKIE_DESKTOP` / `SITEWARM_COOKIE_MOBILE`,
are sent only to `--cookie-hosts` and only over HTTPS. A cookie without
`--cookie-hosts` is an error at startup.

### Exit codes

`0` passed, `1` some result is in `--fail-on`, `2` invalid usage or
configuration, `130`/`143` interrupted by SIGINT/SIGTERM. An interrupt or the
run timeout still writes the report, with unfinished URLs as `incomplete`;
a second Ctrl-C exits at once.

### Using it in CI

Run it after the deploy has finished and the site is live. The exit code is the
result: a step that runs `sitewarm scan` fails when a result is in `--fail-on`
(see [Exit codes](#exit-codes)).

Any shell step:

```
sitewarm scan --from-sitemap https://example.com/sitemap.xml --top 50 \
  --report sitewarm.json
```

GitHub Actions, installing a release and keeping the report:

```yaml
- name: Warm and verify the cache
  env:
    SITEWARM_VERSION: 1.0.2   # the release you want, without the leading v
  run: |
    base=https://github.com/sitewarm/sitewarm/releases/download/v${SITEWARM_VERSION}
    file=sitewarm_${SITEWARM_VERSION}_linux_amd64.tar.gz
    curl -fsSLO "$base/$file"
    curl -fsSLO "$base/checksums.txt"
    grep " ${file}\$" checksums.txt | sha256sum -c -
    tar -xzf "$file" sitewarm
    ./sitewarm scan --from-sitemap https://example.com/sitemap.xml \
      --report sitewarm.json
- uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
  if: always()
  with:
    name: sitewarm-report
    path: sitewarm.json
```

To build from source instead, add `actions/setup-go` (Go 1.26 or newer) and
replace the install lines with
`go install github.com/sitewarm/sitewarm/cmd/sitewarm@latest`.

The run needs outbound HTTPS to your site and, with `--from-sitemap`, to its
`robots.txt`. A run from a CI network is verified at the CDN PoP that serves
that network (see [Limitations](#limitations)).

### JSON report

`--format json` writes the report to stdout and `--report <file>` writes it to
a file. Top-level fields:

| Field | Meaning |
|---|---|
| `tool`, `version` | `sitewarm` and the version that ran |
| `started_at`, `finished_at`, `duration_ms` | timing of the run |
| `stopped` | present when the run ended early: `run-timeout`, `SIGINT`, `SIGTERM` or `canceled` |
| `settings` | the options used: `source`, `top`, `profiles`, `concurrency`, `rps`, `retry_interval`, `max_wait`, `run_timeout`, `fail_on`, and `ignore_robots` when set |
| `summary` | `total`, `by_state` (count per state), `hit_ratio`, `failed`, `passed`, `verdict` |
| `warnings` | standing limitations plus any warning from reading the sitemap |
| `results` | one entry per URL and profile |

Each entry in `results`:

| Field | Meaning |
|---|---|
| `url`, `profile` | the requested URL and `desktop` or `mobile` |
| `lastmod` | with `--from-sitemap`, the `<lastmod>` of the URL as written in the sitemap (a W3C datetime such as `2026-10-01`); left out when the sitemap has none or it is not valid. Informational: it never changes a state or the verdict |
| `final_url`, `redirects` | where it ended up, and the hops (`url`, `status`) |
| `initial_state`, `state` | state of the first request and of the last one |
| `failed` | the result is in `--fail-on` |
| `attempts`, `http_status` | requests made, status of the last one |
| `cdn`, `cache_status_raw`, `pop`, `other_cdns` | CDN that decided, its raw cache header value, the PoP when exposed, other CDNs seen |
| `ttfb_first_ms`, `ttfb_final_ms`, `time_to_hit_ms` | milliseconds, `null` when not measured |
| `cache_control`, `age`, `vary` | response headers |
| `set_cookie` | whether the response had a `Set-Cookie` header (the value is never reported) |
| `indexability`, `indexability_detail` | see [Failing on `noindex`](#failing-on-noindex) |
| `canonical` | the absolute URL in the first `<link rel="canonical">` of the page's `<head>`, resolved against the final URL, at most 2048 bytes; left out when the page has none or the value is not an http(s) URL |
| `warmed` | `true` when the reported measurement came after the URL had already been requested once in this run, or the first response was already a `HIT`; `false` for a single request that was not a `HIT` |
| `detail`, `warnings` | explanation of the state, and per-result warnings |

Fields with no value are left out, except `ttfb_first_ms`, `ttfb_final_ms`,
`time_to_hit_ms` (`null`), `set_cookie`, `failed` and `attempts`.

### Coming from the Python version

What stays: sitemap indexes, including nested ones and `.gz` sitemaps, are
still followed (now with `--from-sitemap`), and results can still be exported
to CSV (now with `--format csv`, to stdout).

What changes on purpose:

- **User-Agent.** Requests use real Chrome navigation headers plus a
  `sitewarm/<version>` token. The Python version sent a Googlebot
  User-Agent; sitewarm never impersonates a crawler.
- **Input.** A CSV of your top pages (`--top-from`) is the main input; the
  sitemap is optional. With a sitemap, `robots.txt` is respected by default.
- **Flags.** `-s/--site`, `-d/--depth`, `-c/--concurrency`, `-o/--output` and
  `-q/--quiet` are gone. Use `sitewarm scan` with `--from-sitemap`,
  `--concurrency`, `--format csv` and `--fail-on`.
- **CSV columns.** `url,status,state` instead of
  `domain,http_code,time,robots_status,cache_control`. Timings, indexability
  and cache headers are in the JSON report (`--format json` or `--report`).

### Update notice

When a newer release exists, `sitewarm scan` prints one line to stderr after
the report, for example `sitewarm: 1.1.0 is available (you have 1.0.2): <release URL>`.
It never installs anything; update with the channel you installed from.

The check is a plain `GET` of the latest release of `sitewarm/sitewarm` on the
GitHub API. It sends no identifiers and no usage data beyond the
`sitewarm/<version>` User-Agent, and GitHub sees the IP address of the
request. The result is cached for 24 hours in your user cache directory
(`sitewarm/update-check.json`), a failure is silent, and it never delays the
run. It is off when stderr is not a terminal, when `CI` or `GITHUB_ACTIONS`
is set, and for builds without a release version. To turn it off, set
`SITEWARM_NO_UPDATE_CHECK=1` or pass `--no-update-check`.

### Limitations

A `hit` is verified only at the CDN PoP serving the machine that runs
sitewarm. A cache key that depends on device or User-Agent without saying so
in `Vary` can make a `hit` unrepresentative of real users.

## Community & support

- **Bugs and ideas:** [open an issue](https://github.com/sitewarm/sitewarm/issues/new/choose).
  A wrong cache state has its own form.
- **Contributing:** see [CONTRIBUTING.md](CONTRIBUTING.md).
- **Security:** report privately, see [SECURITY.md](SECURITY.md).
- **Sponsor:** sitewarm is free and open source. If it saves you a cold cache,
  [sponsor its development](https://github.com/sponsors/ernestova) or
  [star the repo](https://github.com/sitewarm/sitewarm) to follow along.

## License

Apache-2.0. See [LICENSE](LICENSE).
