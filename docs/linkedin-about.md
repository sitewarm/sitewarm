# LinkedIn company page: About (draft)

Draft for the founder to edit and paste into the LinkedIn page himself. Nothing
here is published. LinkedIn limits the About text to 2,000 characters; this one
is about 900.

The old text links to `github.com/clarivo-co/sitewarm`. Use the links below,
which are the current repository, the Action listing and the site.

## About text

```
sitewarm checks, after every deploy, which of your important pages your CDN
serves from cache, and which it does not.

Two open-source tools, both free:

• The sitewarm CLI (Go, Apache-2.0) warms your top pages and verifies the cache
  state per URL and browser profile, on Cloudflare, Vercel, CloudFront, Fastly
  and Akamai. Linux, macOS and Windows.
• The sitewarm GitHub Action runs after each Production deploy and posts a
  GitHub Check with what changed since the last one: pages that stopped being
  served from cache, pages that started to fail, and pages that became noindex.

A check sees the CDN from one vantage point, the machine that runs it, and says
so.

CLI and source: https://github.com/sitewarm/sitewarm
GitHub Action: https://github.com/marketplace/actions/sitewarm
Site: https://sitewarm.dev
```

## Checks before you paste it

- The CLI link must be `github.com/sitewarm/sitewarm`, not `clarivo-co`.
- No performance number appears on purpose: no hit-ratio figure has a dated,
  reproducible run behind it yet.
- The "what changed" line follows the "Red" definition in the Action README (a
  page that became an error or 5xx, became `noindex`, or was a HIT and no longer
  is). If the Action README changes, change this line with it.
- The website field of the page can carry `https://sitewarm.dev` as well.
