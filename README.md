# imgssh

imgssh is a process-local SSH wrapper that uploads local clipboard images to a remote host and inserts the quoted file path into your current SSH session.

```text
copy a local image
connect with imgssh
press Ctrl+]
get '/tmp/imgssh-....png' inserted into the current SSH session
```

No terminal plugin. No daemon. No remote install. No global keyboard hook. Use it instead of `ssh`.

## Install

This is a fork of [coderredlab/imgssh](https://github.com/coderredlab/imgssh) with fixes for escape-sequence handling over tmux/SSH — see [Fork changes](#fork-changes).

```bash
go install github.com/YanzuoLu/imgssh/cmd/imgssh@latest
```

Or from a source checkout:

```bash
go install ./cmd/imgssh
```

## Usage

Use `imgssh` like `ssh`:

```bash
imgssh coder@dev
imgssh -p 2222 coder@dev
imgssh -i ~/.ssh/id_ed25519 coder@dev
imgssh -J bastion coder@dev
imgssh coder@dev tmux attach
```

Copy a PNG image locally, focus the SSH session, then press:

```text
Ctrl+]
```

On success, imgssh inserts a shell-quoted remote path at the current cursor position:

```text
'/tmp/imgssh-20260425-142744-3aa0927c.png'
```

It does not press Enter for you.

## How It Works

imgssh launches `ssh` through a local PTY and relays input/output between your terminal and the SSH process. Input is parsed into whole escape sequences and each is forwarded in a single write, so mouse, paste and terminal-query replies are never split mid-sequence (see [Fork changes](#fork-changes)).

When `Ctrl+]` reaches the imgssh process, imgssh:

```text
1. reads a PNG image from the local clipboard
2. uploads it to the remote host through a separate ssh process
3. writes the shell-quoted remote path into the current SSH PTY
```

The upload connection reuses a temporary OpenSSH `ControlPath` created for the interactive session. This means password authentication can work after the initial login, without prompting again during image upload.

## Options

```text
--remote-dir <path>          remote upload directory (default: /tmp)
--ssh-bin <path>             ssh binary path (default: ssh, or IMGSSH_SSH_BIN)
--upload-timeout <duration>  upload timeout (default: 30s)
--max-size <size>            max PNG size (default: 25MB)
--no-inject                  upload without inserting the remote path
--quiet                      suppress local failure messages
--version                    print version
--help                       print help
```

## Environment

```text
IMGSSH_SSH_BIN=<path>        default ssh binary (overridden by --ssh-bin)
IMGSSH_DEBUG_INPUT=<file>    append a hex dump of received input (debugging)
```

## Clipboard Support

imgssh reads PNG images only.

```text
Wayland: wl-paste --type image/png
X11:     xclip -selection clipboard -t image/png -o
X11:     xsel --clipboard --output --mime-type image/png
macOS:   pngpaste -
```

Install one matching clipboard backend before using image paste.

## Boundaries

imgssh only reacts to bytes received through its own stdin. It does not:

```text
- install global keyboard hooks
- detect active windows, tabs, or panes
- manipulate other terminal sessions
- run as a background daemon
- create SSH reverse tunnels
- let the remote server request local clipboard contents
```

Nested SSH sessions are not tracked. If you run `imgssh dev` and then run `ssh prod` inside `dev`, image uploads still go to `dev`.

## Fork changes

This fork rewrites the input relay to parse terminal input into whole escape sequences — CSI control sequences and OSC/DCS/APC/PM/SOS string sequences — and forward each one in a single write. The upstream relay buffered input byte-by-byte while watching for the `Ctrl+]` trigger, which could split escape sequences and, over tmux/SSH, caused:

```text
- stray characters when scrolling a tmux pane with the mouse
- leaked terminal-query replies (e.g. an OSC 11 "rgb:" colour response
  printed on screen) when an editor opened inside tmux
```

Trigger detection is unchanged: `Ctrl+]` is still recognized both as the raw `0x1d` byte and as its CSI-u encodings (`\x1b[93;5u`, `\x1b[27;5;93~`).

Set `IMGSSH_DEBUG_INPUT=<file>` to append a timestamped hex dump of all received input, for diagnosing similar issues.

## Troubleshooting

If pressing `Ctrl+]` does nothing, make sure the terminal focus is inside the `imgssh` session and not in another local pane.

If upload fails with an authentication error, check that the interactive session is still alive and that your OpenSSH client supports ControlMaster/ControlPath.

If clipboard reading fails, verify that your environment has one supported PNG clipboard backend installed and that the clipboard currently contains a PNG image.
