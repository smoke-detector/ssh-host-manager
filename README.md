# SSH Host Manager

A small Windows app that sets up passwordless SSH to your servers and devices, so that
you and AI apps (Claude, ChatGPT, anything that can run a shell command) can run
`ssh <name> "<command>"` with no passwords or prompts. The server's address works in
place of the name: `ssh 203.0.113.10 "<command>"`.

> **This app only talks to the servers you add.** It does not talk to anything in the
> cloud or any other service. That is why the source code is free to view: anyone can
> check. The one exception is the donation link, which opens in your browser if you
> click it, because my AI is breaking my bank.

Made by ITEAdvisors. Free to use. [Donate](https://donate.stripe.com/eVq6oz0LU7ucgX19KE57W00) if it helps you.

![SSH Host Manager](docs/server.png)

**[Read the manual](MANUAL.md)**

## What it does
- Creates a dedicated key for each server and installs it after you type the password once.
- Works with Linux, macOS, NAS, VMware ESXi, Windows OpenSSH Server, and network devices.
- Verifies each server's fingerprint before trusting it, and lets you remove host keys
  one at a time.
- Keeps everything in standard OpenSSH files (`config`, `known_hosts`, key files), in
  `~/.ssh`, OneDrive, or a folder you pick.
- Can allow older encryption for one legacy device without weakening the others.

## How you can check the privacy claim
- The window is drawn by the app itself. There is no browser, web view or embedded
  web page, and no HTTP client is compiled into the program.
- The only network code is in `tools.go`: a TCP check of the address you entered, and
  the `ssh`, `ssh-keygen` and `ssh-keyscan` programs that come with Windows.
- The only web address in the source is the donation link in `about.go`. It is handed
  to your default browser when you click it; the app never contacts it.
- Watch it yourself: open Resource Monitor, go to the Network tab, and tick
  `SSHHostManager.exe` and `ssh.exe`. You will only see the servers you added.

## What it manages
- `config`: only the block between the `# >>> SSH Server Manager` markers is
  rewritten. Everything else is kept as-is, and `config.bak` is saved before each write.
  Each server is written as `Host <name> <address>`, so both forms use the key.
- `known_hosts`: host keys are shown, verified by fingerprint before trusting,
  replaced when a server changes, and cleaned up when a server is deleted or moved.
- Per-server keys (`id_ed25519_<name>` or `id_rsa_<name>`), no passphrase so apps can
  use them unattended.

When the app works in a folder other than `%USERPROFILE%\.ssh`, it adds a small
`# >>> SSH Server Manager: app folder` block with an `Include` line at the top of
`%USERPROFILE%\.ssh\config`, names the folder's `known_hosts` per server, and restricts
the permissions of the config and keys it writes there (ssh refuses them otherwise).
The folder choice is remembered in `%LOCALAPPDATA%\SSHHostManager\settings.json`.

## Build
Requires Go 1.26+ on Windows. The window uses [Gio](https://gioui.org), a pure-Go
drawing toolkit; no C compiler is needed.

    go run github.com/tc-hib/go-winres@latest make --in winres/winres.json --arch amd64   # icon, manifest, version info
    go build -trimpath -ldflags "-H windowsgui -s -w" -o SSHHostManager.exe .

| File | What is in it |
| --- | --- |
| `app.go` | Saving, deleting, scanning and trusting servers |
| `tools.go` | Running `ssh`, `ssh-keygen`, `ssh-keyscan`; the key-install commands |
| `sshconfig.go`, `knownhosts.go` | Reading and writing the OpenSSH files |
| `folders.go` | Which folder the app works in |
| `ui.go`, `ui_kit.go`, `ui_flows.go` | The window, its controls, and what the buttons do |
| `exec_windows.go` | Terminal window, folder chooser, file permissions |

For testing, set `SSHKEYS_SSH_DIR` to an empty folder and the app treats it as `~/.ssh`
instead of the real one. `SSHKEYS_ONEDRIVE_DIR` and `SSHKEYS_DATA_DIR` do the same for
OneDrive and for the app's settings folder.

Runtime requirements: Windows 10 or 11 with the OpenSSH Client (built in).
