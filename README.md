# gogloader

Download and install GOG.com games (including multipart installers) by store name.

## Build

    go build -o gogloader ./cmd/gogloader
    # or install into $GOBIN:
    go install ./cmd/gogloader

## Development

Tools are pinned in `mise.toml`:

    mise install              # go, golangci-lint, prek
    mise run hooks:install    # set up the git pre-commit hook (fmt, lint, test)
    mise run check            # run fmt, lint and test by hand

Each task can also be run on its own: `mise run fmt`, `mise run lint`, `mise run test` or `mise run build`.

## Usage

    gogloader auth login                  # one-time browser login, token stored in your config dir
    gogloader auth                        # show login status
    gogloader auth logout
    gogloader list [filter]               # games in your library (cached 10 min; -r to refresh)
    gogloader info kenshi                 # installers, versions, part counts
    gogloader download https://www.gog.com/en/game/kenshi
    gogloader install kenshi --dest ~/Games

`<game>` can be a store URL, a store slug, a product id or a title.

Every command has `--help`. To set up shell completions, run `gogloader completion <bash|zsh|fish|powershell>`.

### Flags (download / install)

| flag | default | |
|---|---|---|
| `--os` | host OS, falls back to `windows` | `windows`, `mac`, `linux` |
| `--lang` | `en` | falls back to English if missing |
| `--dir`, `-d` | `./<slug>` | folder the installer files are saved in (used as-is when set) |
| `--parallel`, `-p` | `4` | parts downloaded concurrently |
| `--retries` | `5` | per part; links are re-resolved each retry |
| `--dlc` | off | also fetch/install owned DLCs |
| `--dest` (install) | `~/Games` | game goes into `<dest>/<slug>/` |
| `--wine` (install) | off | run Windows setup under wine instead of innoextract |
| `--keep` (install) | `true` | keep installer files afterwards |

Downloads resume from `.part` files. Parts are checked against GOG's md5 when one is published.

### How installing works

- **Windows installers** (`setup.exe` + `-1.bin`, `-2.bin` …): runs silently on Windows. On macOS/Linux it unpacks them with
  [`innoextract`](https://constexpr.org/innoextract/) (`brew install innoextract`), or with `--wine` it runs the installer under wine.
- **Linux `.sh`**: extracts the embedded `data/noarch` payload directly. This works on any host and nothing is interactive.
- **macOS `.pkg`**: `pkgutil --expand-full`, then the payload is copied into the destination.

## Where your login is stored

| OS | Default store |
|---|---|
| macOS | login Keychain, generic password `gogloader` / `oauth-token` |
| Windows | Credential Manager (DPAPI-encrypted, per user) |
| Linux | Secret Service: GNOME Keyring, KWallet or KeePassXC, over D-Bus |

When no keyring is available (for example headless Linux with no D-Bus session), the token falls back to a `0600` file in a `0700` folder. On Windows that file is under `%LocalAppData%`, so it isn't copied to other machines by a roaming profile. A token file left over from an older version moves into the keyring automatically the next time the tool runs. `auth` shows which store is in use.

To force one store, set `GOGLOADER_TOKEN_STORE=keyring` (an error instead of falling back) or `GOGLOADER_TOKEN_STORE=file`.

`auth logout` deletes the token from every store. GOG has no public way to revoke a token, so a copy taken earlier would keep working.
