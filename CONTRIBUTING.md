# Contributing

Contributions are welcome. Please read this before submitting a pull request.

## Before You Start

Open a [discussion](https://github.com/x0lie/pia-tun/discussions) before implementing a feature. This avoids wasted effort if the feature doesn't fit the project's scope.

## Pull Requests

1. Fork the repository and create a branch from `develop`
2. Make your changes
3. Run the integration tests (see below)
4. Submit a pull request targeting `develop`, not `main`

### Style

- Follow [Conventional Commits](https://www.conventionalcommits.org/) for commit messages (`fix:`, `feat:`, `chore:`, etc.).
- Keep functions small and focused, files and packages well-scoped, and exports minimal. Prefer clarity over cleverness.

### Integration Tests

The test suite requires a real PIA account. Set credentials via environment variables:

```bash
PIA_USER=your_username PIA_PASS=your_password go test -v -tags integration -timeout 5m ./tests/integration/...
```
