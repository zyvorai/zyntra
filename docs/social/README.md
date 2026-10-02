# Zyntra social and README images

Everything here is rendered from HTML; nothing is drawn by hand and nothing in the console screenshots is mocked. The social card keeps the original look (dark violet-tinted background, violet-to-orange headline, four step cards); the README cards use the Zyvor brand (ink `#111`, signal orange `#ff5a15`, white, Z mark).

## Social card

| File | Size | Use |
| --- | --- | --- |
| `zyntra-social-3200x1680.jpg` / `.png` | 3200×1680 | Master for LinkedIn, X and print. Crop-safe for 1.91:1 previews |
| `zyntra-share-2400x1260.png` | 2400×1260 | README hero and Open Graph |
| `zyntra-github-preview-1280x640.png` | 1280×640 | **GitHub social preview**. Upload by hand |

Source: `zyntra-social-hires.html` (designed at 1600×840). Rebuild with `./docs/social/build-social-card.sh` (needs Google Chrome and macOS `sips`; nothing is installed).

GitHub does not let you set the social preview through the API. After changing the card, upload `zyntra-github-preview-1280x640.png` in the repository's **Settings → General → Social preview**.

## README images

| Source | Output (`docs/ux/`) | Width |
| --- | --- | --- |
| `readme/{loop,capabilities,ontology,packs,safety,tenants-rollouts,deploy,editions}.html` | `readme-*.jpg` | 3200 px (1600 CSS px at 2×) |
| `readme/frame.html` around a real console capture | `shot-*.jpg` | 3200 px |

Shared styles are in `readme/brand.css`. To rebuild the cards and frames:

```bash
node docs/social/readme/build.cjs              # all of them, or name some: loop shot-plan
```

It uses Playwright (`web/node_modules/playwright` or `PLAYWRIGHT=/path/to/playwright`) with installed Google Chrome, and macOS `sips` for JPEG quality 90.

The console frames wrap real captures from a running lab. `readme/capture.cjs` takes them (the steps are in its header) into `readme/_captures/`, which is not committed. Use a fresh `ZYNTRA_STATE_DIR` per pack so objects and proposals do not mix.
