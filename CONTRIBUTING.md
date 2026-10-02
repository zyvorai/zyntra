# Contributing

Thanks for helping improve Zyntra.

1. Fork the repository and create a focused branch.
2. Run `gofmt -w .` on Go changes.
3. Add tests for any change to gap scoring, propagation, ranking, constraints, preconditions, invariants, revalidation, outcome verification, sources, executors, policy or authentication.
4. Run `make check` and `make test-e2e`.
5. Keep recommendations explainable: every number Zyntra shows must be traceable to an input, an edge or an action effect.

## Adding a pack

A pack is files only; it must not need engine changes.

1. Create `packs/<id>/` with `pack.yaml`, `kpis.yaml`, `sources.example.yaml`, `README.md` and a `fixture/` of sample exports. Use [packs/shop](packs/shop) as the template.
2. The README names the three gaps the pack is judged on and shows one `zyntra simulate` trace.
3. Document every `${ZYNTRA_*}` variable in `sources.example.yaml`. Zyntra's own credentials (`ZYNTRA_API_KEY`, tokens, secrets) are never expanded.
4. Give weights a `why` and an honest `confidence`; they are starting points, not truth.
5. Run `zyntra pack validate packs/<id>`. `TestEveryPackValidates` runs it for every pack in CI.

A pack that needs a new source kind is rejected until that kind is generic. No pack may recommend a clinical, legal or credit decision about a person: levers are capacity, queues, price lists and routes.

## Dependencies

Zyntra keeps its dependency list short so the binary is easy to audit. The core needs only `gopkg.in/yaml.v3`. Authentication is the one exception: `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2` implement OpenID Connect (ID-token verification, discovery, PKCE) and `golang.org/x/crypto/bcrypt` hashes local passwords. Hand-rolled versions of these would be a security risk. Any other new dependency needs a clear reason in the pull request.

Contributions are accepted under the Zyvor Production License. Include the `LicenseRef-Zyvor-Production-1.0` SPDX header in new source files.
