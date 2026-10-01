# Zyntra social assets

| File | Size | Use |
| --- | --- | --- |
| `zyntra-share-card.png` | 1200×630 | README hero and GitHub social preview (Settings → General → Social preview) |
| `zyntra-social-card.jpg` | 1600×900 | LinkedIn and X posts |
| `readme/*.html` → `../ux/readme-*.jpg` | 1600 wide | Section images in the README |

The images are rendered from the HTML next to them. To change copy, edit the HTML and rebuild:

```bash
./docs/social/build-social-card.sh   # share card + social card (Chrome + sips)
./docs/social/readme/build.sh        # README section cards
```

Both scripts only need Google Chrome and macOS `sips`; nothing is installed.

GitHub does not let you set the social preview through the API, so upload `zyntra-share-card.png` by hand after changing it.
