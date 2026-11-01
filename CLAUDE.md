# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Go CLI (only dependency: spf13/cobra for the command tree; `internal/` packages are stdlib-only) that downloads and installs GOG.com games, including multipart installers, by store URL/slug. User-facing docs and the flag table are in README.md.

## Commands

Tools (Go, golangci-lint, prek) are pinned in `mise.toml`, and every dev command is a mise task:

```sh
mise install          # install pinned tools
mise run fmt          # golangci-lint fmt (gofmt + goimports, local-prefix github.com/stvnksslr/gogloader)
mise run lint         # golangci-lint run (config: .golangci.yml)
mise run test         # go test ./...
mise run check        # fmt → lint → test
mise run build        # ./gogloader
mise run release:check     # goreleaser check
mise run release:snapshot  # build all release archives into dist/ (no publish)
mise run lint:actions      # actionlint on .github/workflows
mise run hooks:install  # prek install (git pre-commit hook)
mise run hooks:run      # prek run --all-files
go test ./internal/download -run TestFetchMultipartResumeAndVerify -v   # single test
```

The pre-commit hooks (`.pre-commit-config.yaml`, run by prek) are local `language: system` hooks that only call the mise tasks above. Change behavior in `mise.toml`, not in the hook config. Lint uses the `standard` linter set plus a few extras, with the `std-error-handling` exclusion preset (unchecked `Close`/`Remove`/`fmt.Fprint*`). In tests, check setup errors with the package-local `must(t, err)` helper rather than ignoring them.

The download test takes ~6s because the md5-mismatch case exercises the retry backoff.

Manual testing against the live API needs a token: `./gogloader auth login` (browser OAuth, paste back the redirect URL), then `./gogloader auth` to check status. Token lives at `os.UserConfigDir()/gogloader/token.json`.

## Releases

release-please (`release-please-config.json`, `.release-please-manifest.json`) keeps a release PR open from conventional commits on `main`. Merging it tags `vX.Y.Z`, and the `goreleaser` job in `.github/workflows/release.yml` (gated on `release_created`) runs `mise run release`, which attaches assets to the existing release (`release.mode: keep-existing`, changelog disabled) and pushes a cask to `stvnksslr/homebrew-tap`.

- Don't edit `CHANGELOG.md` or the manifest by hand; release-please owns them.
- PR titles must be conventional, because squash merge makes the title the commit on `main`.
- Version info is injected via `-X main.version/commit/date` into `cmd/gogloader/main.go`. `internal/cli/version.go` falls back to `debug.ReadBuildInfo` for `go install`/`go build` binaries.
- Workflow actions are pinned to commit SHAs with a `# vX.Y.Z` comment. Bump the SHA and the comment together. `mise run lint:actions` (actionlint) runs in CI and as a prek hook.

## Architecture

Layout: `cmd/gogloader/main.go` is a thin entrypoint (signal-aware context, then `cli.Execute`). Everything else is under `internal/`.

- `internal/cli` — the cobra command tree, one file per command (`root.go`, `auth.go`, `list.go`, `info.go`, `download.go`, `install.go`), plus `output.go` for shared output helpers. Commands read the context via `cmd.Context()`. `auth` runs `status` when given no subcommand. `download` and `install` share `fetchOpts.register` for flags and `fetch()` (in `download.go`) for resolve and download; `install` adds its own flags and then calls `internal/install`. To add a command, write a `newXxxCmd()` constructor and register it in `NewRootCmd`. The download folder defaults to `./<slug>`, and `--dir` is used as given.

Flow: **resolve name → product → select installer → download parts → install**.

- `internal/gog` — auth + API client.
  - Token storage (`store.go`): OS keyring via `zalando/go-keyring` first, then a hardened file fallback (`CreateTemp`+rename, `0600` file and `0700` folder, regular files only, loose permissions tightened on load; `%LocalAppData%` on Windows). `GOGLOADER_TOKEN_STORE=auto|keyring|file`. In auto mode an existing token file is moved into the keyring and deleted. Storage notices go through `gog.Warnf`, which `cli` sets to its own `warnf`. Tests must call `keyring.MockInit()` / `MockInitWithError()` (see `store_test.go`) so they never touch the real keychain.
  - OAuth uses the public GOG Galaxy client id/secret (same as lgogdownloader/heroic). `Client.accessToken` auto-refreshes under a mutex since parts resolve concurrently.
  - `Resolve` matches a name against the owned library (`embed.gog.com/account/getFilteredProducts`) by id → exact slug → squashed title/slug; if nothing matches it asks the public catalog (`catalog.gog.com`) because store slugs can differ from library slugs, then falls back to substring candidates (`AmbiguousError`).
  - Installer data comes from `api.gog.com/products/{id}?expand=downloads,expanded_dlcs`. Each `Installer.Files` entry is one part of a multipart installer. `downlink` URLs need auth and return JSON `{downlink, checksum}`; the checksum URL is XML with an `md5` attribute (best-effort, may be empty).
  - Library listing is cached (`cache.go`) at `os.UserCacheDir()/gogloader/library.json` for `LibraryTTL` (10m), keyed by the token's user id. `list --refresh` bypasses it, `Resolve` refetches once on an exact-match miss so recent purchases still resolve, and `Logout` clears it. Product/installer data and download links are never cached.
- `internal/download` — generic parallel downloader, no GOG knowledge. `Part.Resolve` is called on **every attempt** because CDN links are signed and expire. Writes to `<name>.part`, resumes with `Range`, renames on completion, so an existing final file means complete (re-verified by md5 when available). Filename comes from the CDN URL path.
  - **GOG's API `size` values are rounded/approximate** (e.g. exactly 1 MiB for a setup.exe). They are used only for progress estimates; completeness is checked against `Content-Length`/`Content-Range`. Don't reintroduce size-equality checks against the API value.
- `internal/install` — per installer OS:
  - `windows`: Inno Setup `.exe` + `.bin` parts. Runs silently on a Windows host; otherwise `innoextract --gog` into a temp dir next to dest, flattening its `app/` dir (or `--wine`).
  - `linux`: the `.sh` is a shell stub + zip; `archive/zip` reads it directly (handles prepended data) and extracts only `data/noarch/`, with zip-slip and absolute-symlink guards.
  - `mac`: `pkgutil --expand-full`, then moves every `Payload` dir's contents. macOS host only.
  - `mergeMove` layers results into dest so DLC installs overlay the base game.

- `internal/ui` — terminal output helpers used by every command. `ui.For(w)` styles only on a TTY (respects `NO_COLOR`; `CLICOLOR_FORCE=1` forces it, which is the way to preview styled output from a non-TTY shell). `ui.Table` aligns by visible width, so cells may be pre-styled; `MaxWidth` truncation applies only on a TTY so piped output stays complete. Use these rather than `fmt` padding verbs, which miscount ANSI escapes and multi-byte runes.

Platform default: host OS, falling back to `windows` if the game has no native build (common, e.g. Kenshi), which on macOS/Linux requires `innoextract`.
