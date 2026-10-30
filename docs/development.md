# Development

Hane uses one Go module. The source is organized into:

```text
cmd/hane/           CLI entry point
internal/app/       Terminal UI, accounts, mail, and credential handling
internal/app/assets/ Embedded terminal logo
internal/buildinfo/ Shared application version
internal/cli/       Command parsing, option validation, and help
internal/update/    Release discovery, verified downloads, atomic installation
scripts/            Release packaging and screenshot rendering
docs/               Usage guide and screenshots
```

Tests live beside the code they exercise. Existing mail settings and cache paths retain the original `sorakyaku` namespace for compatibility; see [storage](usage.md#storage).

## Build and test

```sh
make build              # ./hane
make run
make install            # ~/.local/bin/hane
make check              # tests and go vet
GOCACHE=/tmp/hane-go-build go test -race ./...
```

`PREFIX` changes the install prefix, and `DESTDIR` supports staged installs. For example, `make install PREFIX=/usr/local` installs into `/usr/local/bin` when permissions allow it.

Tests use local IMAP/SMTP fixtures and a local HTTP release server. They do not connect to real mail accounts or send external messages. macOS and Linux run tests in GitHub Actions; terminal interaction still needs testing on each platform.

## Release

1. Update `internal/buildinfo/version.go` to the new `MAJOR.MINOR.PATCH` version.
2. Run `make check` and `make release`.
3. Commit the release, then push a matching `vMAJOR.MINOR.PATCH` tag.

The release workflow builds Linux and macOS archives for `amd64` and `arm64`, uploads all four archives plus `checksums.txt` to a draft GitHub release, then publishes it. Keeping the release in draft until uploads finish prevents `hane update` from selecting a release with missing assets.

`make release` writes archives to `dist/`, which is excluded from Git. To build the current version locally:

```sh
make release
(cd dist && sha256sum -c checksums.txt)  # macOS: shasum -a 256 -c checksums.txt
```

Each archive contains only a `hane` executable. Asset names follow `hane_VERSION_OS_ARCH.tar.gz`; the updater and packaging script share that convention. Windows releases are not currently provided.

## CLI behavior

`cmd/hane` dispatches one action after `internal/cli` parses and validates the complete invocation. Options work before or after the command. Invalid combinations exit with status 2 before opening accounts, mail storage, or making update requests. An action failure exits with status 1; success and help exit with status 0.

`internal/app` receives interactive options directly; it does not parse process-wide flags. Version and help commands use no mail settings. The private `__keyring` and `__passwords` subprocess entry points remain separate from public commands so keyring calls and the UI's password wizard keep working.

## Update behavior

`hane update` runs before the app opens its accounts or mail store. It requests the latest stable release from `dmillzilla/hane`, compares numeric versions, selects the matching platform archive, and verifies the archive against the release's SHA-256 manifest.

The updater resolves symlinks, locks the target against simultaneous updates, writes a temporary executable in the same directory, then atomically renames it into place. A download, checksum, unpacking, permission, or cancellation failure leaves the old executable in place. It also refuses to overwrite a binary replaced by another installer while the download was in progress. System-owned installs require reinstalling with suitable permissions; the updater does not elevate privileges.

## Changes to main

Open a pull request for every change to `main`, including administrator changes. Both Linux and macOS CI checks must pass against the latest base branch, and review conversations must be resolved. Force pushes and deletion of `main` are blocked.

David (`@dmillzilla`) owns all files through `.github/CODEOWNERS`. Non-admins need one approving code-owner review; new reviewable commits dismiss stale approvals. Repository admins can explicitly bypass the review requirement when merging a PR, including their own PRs. That exception does not bypass the PR requirement, CI checks, or conversation resolution.

GitHub applies bypass permission to the person merging, rather than to the PR author. Admins should approve contributor PRs before merging; the review bypass exists so admin-authored PRs do not need self-approval.
