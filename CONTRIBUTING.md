# Contributing

Thanks for helping improve Zyntra.

1. Fork the repository and create a focused branch.
2. Run `gofmt -w .` on Go changes.
3. Add tests for any change to gap scoring, propagation, ranking, constraints, revalidation, outcome verification, policy or authentication.
4. Run `make check` and `make test-e2e`.
5. Keep recommendations explainable: every number Zyntra shows must be traceable to an input, an edge or an action effect.

## Dependencies

Zyntra keeps its dependency list short so the binary is easy to audit. The core needs only `gopkg.in/yaml.v3`. Authentication is the one exception: `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2` implement OpenID Connect (ID-token verification, discovery, PKCE) and `golang.org/x/crypto/bcrypt` hashes local passwords. Hand-rolled versions of these would be a security risk. Any other new dependency needs a clear reason in the pull request.

Contributions are accepted under the Zyvor Production License. Include the `LicenseRef-Zyvor-Production-1.0` SPDX header in new source files.
