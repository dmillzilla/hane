# Hane

A TUI email client with Vim-style navigation.

Read conversations, manage multiple accounts, write replies, and keep drafts between sessions. Hane connects to IMAP and SMTP servers, including iCloud and Gmail with app passwords.

![Hane inbox](docs/live-mail.png)

## Install

Download the archive for your computer from [the latest release](https://github.com/dmillzilla/hane/releases/latest):

| Computer | Archive suffix |
|---|---|
| Linux, x86-64 | `linux_amd64.tar.gz` |
| Linux, ARM64 | `linux_arm64.tar.gz` |
| Mac, Apple Silicon | `darwin_arm64.tar.gz` |
| Mac, Intel | `darwin_amd64.tar.gz` |

Extract the archive, open a terminal in the directory containing the extracted `hane` binary, and install it:

```sh
mkdir -p ~/.local/bin
install -m755 hane ~/.local/bin/hane
export PATH="$HOME/.local/bin:$PATH"
hane
```

Keep `~/.local/bin` on your shell's PATH to launch Hane from any directory. Linux uses your desktop's Secret Service keyring; macOS uses Keychain.

To build from source instead, install Go 1.27.1 or newer and Make, then run:

```sh
git clone https://github.com/dmillzilla/hane.git
cd hane
make install
hane
```

## Add your email account

1. Launch `hane` and press **A** to add an account.
2. Choose iCloud, Gmail, or Custom, then enter your email and app password.
3. Select **Connect & save**. Hane checks the connection and opens your inbox.

Use an app-specific password for [iCloud](https://support.apple.com/en-us/102525) or an eligible [Gmail account](https://support.google.com/accounts/answer/185833?hl=en). OAuth-only accounts are not supported yet. To remember a password through Bitwarden and the local keyring, press **B** after adding the account.

Want to try it first? Run `hane demo` for a sample inbox that never connects or sends mail.

## Everyday use

Use these keys to navigate Hane's TUI:

| Keys | Action |
|---|---|
| `j` / `k` | Move down / up |
| `Enter` / `l` | Open a folder or conversation |
| `h` / `Esc` | Go back |
| `Space n t` | Show or hide the account explorer |
| `/` | Search downloaded mail |
| `r` / `c` | Reply / compose a new message |
| `u` / `s` | Toggle unread / starred |
| `dd` | Move a conversation to Trash after confirmation |
| `R` / `L` | Sync now / load older messages |
| `A` / `B` | Add account / set up remembered passwords |
| `?` | Show the keymap |
| `q` | Close the conversation or quit |

In the editor, press **i** to type and **Esc** to return to Normal mode. Use **`:to friend@example.com`** and **`:subject Hello`** to set a new message's headers. **Ctrl+s** saves; **Ctrl+y** sends. Drafts autosave locally, and closing the editor offers to save or discard unsaved changes.

Mail syncs in the background every minute. Opening a conversation marks it read; previews do not. Use `hane --offline` to read cached mail and write drafts without connecting.

For the complete keymap, account setup, and delivery recovery instructions, see the [usage guide](docs/usage.md).

## Update

```sh
hane update
```

Downloads the latest stable release for your operating system and processor, verifies its SHA-256 checksum, and replaces the executable you launched. Accounts, saved passwords, mail, and drafts stay in place. If you already have the latest version, no binary is downloaded.

The install directory must be writable by your user; `~/.local/bin` works well. If Hane is installed in a system-owned directory, reinstall the new binary there with the appropriate permissions.

## Commands and options

Commands choose an action; options adjust that action. Combine compatible options in one invocation, before or after the command.

| Command | Purpose | Options / aliases |
|---|---|---|
| `hane` | Open your mailbox | `--offline` / `-o`, `--no-splash` |
| `hane demo` | Open sample mail | `--no-splash`; alias `-d` |
| `hane version` | Print the installed version | Alias `-v` |
| `hane update` | Install the latest stable release | |
| `hane sync` | Sync once | `--account EMAIL` / `-a EMAIL` |
| `hane check` | Check IMAP and SMTP sign-in | `--account EMAIL` / `-a EMAIL` |
| `hane passwords` | Set up remembered passwords | `--account EMAIL` / `-a EMAIL` |
| `hane help [command]` | Show help | Also `--help` / `-h` |

```sh
hane --offline --no-splash
hane demo --no-splash
hane -d --no-splash
hane sync -a me@example.com
hane --account me@example.com check
hane help passwords
```

`--account` limits `sync` or `check` to one account; without it, all configured accounts are used. The password wizard asks which account to use when needed. Commands run one at a time; use your shell to sequence them, such as `hane check && hane sync`.

The previous `--demo`, `--version`, `--sync-once`, `--check-connection`, and `--password-setup` flags remain supported as command aliases.

Connection checks and `hane sync` do not send outgoing messages. Search covers downloaded mail; use **L** to load more. Attachments are listed but cannot yet be downloaded or attached, and composition is plain text. Drafts are stored locally rather than in the server's Drafts folder.

See [development](docs/development.md) for the source layout, tests, and release builds.
