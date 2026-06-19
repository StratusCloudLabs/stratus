# Contributing to stratus-runtime

Thanks for your interest in contributing to **`stratus-runtime`** — the
self-hostable, single-binary Go runtime for orchestrating ephemeral GitHub
Actions runners and hardware-isolated Kata sandboxes on Kubernetes. Community
contributions are welcome.

Please also read our [Code of Conduct](CODE_OF_CONDUCT.md) and our
[Security Policy](SECURITY.md). Do **not** file security vulnerabilities as
public issues — follow [SECURITY.md](SECURITY.md) instead.

## Ways to Contribute

- Reporting bugs and proposing features (see the issue templates).
- Improving documentation.
- Fixing bugs and implementing features via pull requests.

## Before You Start — Issue First for Big Changes

For anything beyond a small fix (new features, behavioral changes, refactors,
or anything that touches the controller/claim semantics or the public HTTP
API), **open an issue first** to discuss the design and get alignment before
investing in a large change. This avoids wasted work and duplicated effort.
Small, obvious fixes (typos, docs, tiny bug fixes) can go straight to a PR.

## Development Workflow

1. **Fork** the repository and create a topic branch from `main`.
2. Make your change in a focused, self-contained branch — one logical change per
   pull request. Avoid mixing unrelated changes.
3. **Build and test locally** before pushing. Per the
   [README](README.md), the standard checks are:

   ```bash
   go build ./...    # build all packages
   go vet ./...      # static analysis
   go test ./...     # run the test suite
   ```

   CI runs these same checks (see `.github/workflows/ci.yml`) plus a secret
   scan, so green local checks mean a smoother review.
4. Keep commits clean and descriptive, and **sign off** every commit (see DCO
   below).
5. Open a pull request using the
   [pull request template](.github/PULL_REQUEST_TEMPLATE.md) and fill in every
   section. Reference the issue it addresses.

## Pull Request Expectations

- Keep PRs small and focused; large or unrelated diffs are hard to review.
- Update documentation when behavior or configuration changes.
- Ensure `go build`, `go vet`, and `go test` all pass.
- Be responsive to review feedback.

## Developer Certificate of Origin (DCO)

Contributions are accepted under the project's **Apache License 2.0**. We use
the **Developer Certificate of Origin (DCO)** to certify that you wrote, or
otherwise have the right to submit, the code you contribute.

To certify your contribution, **sign off** each commit:

```bash
git commit -s -m "your commit message"
```

This adds a `Signed-off-by: Your Name <your@email.com>` trailer to the commit
message, indicating your agreement with the DCO. Read the full text at
<https://developercertificate.org/>.

> A CLA may be introduced later; until then contributions are under Apache-2.0
> via DCO.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE) that covers this project.
