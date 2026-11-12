# Contributing

Thanks for wanting to improve hoard. Bug reports, ideas and pull requests are welcome.

## Before you start

For anything larger than a small fix, open an issue first so the direction is agreed before you invest time. Security problems go through [SECURITY.md](SECURITY.md), not the issue tracker.

## Development setup

Requirements: Go 1.27+, Node 24+.

```bash
make build
```

```bash
make run
```

```bash
make test
```

`make run` serves on <http://localhost:8080> with data in `./data`. For UI work, keep it running and start `npm --prefix web run dev` in a second terminal.

[CLAUDE.md](CLAUDE.md) describes the layout and the conventions in more detail; it is written for AI coding assistants but is just as useful for people.

## Pull requests

- **The title must be a [Conventional Commit](https://www.conventionalcommits.org)**, for example `fix(mcp): reject empty ids`. Pull requests are squash-merged, the title becomes the commit, and the release version is derived from it: `feat` is a minor release, `fix` and `perf` a patch, a `!` after the type a breaking change. `docs`, `ci`, `chore`, `refactor`, `test` and `build` release nothing. A check enforces the format.
- Keep a pull request to one change. Update the docs (`README.md`, `docs/`) in the same pull request as the behaviour.
- `make test` (Go tests, the web UI tests with vitest, the release-version tests) and `npm --prefix web run typecheck` must pass. UI logic and the primitives in `web/src/ui` are tested with vitest and Testing Library (`npm --prefix web test`, files named `*.test.ts(x)` next to the code); a test must not need a running server. Add or extend tests for behaviour you change; the existing tests exercise the service and the HTTP and MCP layers for real, which is the style to follow.
- New UI controls, colours and sizes belong in the design system first (`web/src/styles`, `web/src/ui`), and should show up on the `/design` page.

## License

hoard is released under the [MIT License](LICENSE). By contributing you agree that your contribution is licensed under the same terms.
