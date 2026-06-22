package imgssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const (
	pasteTriggerSequence = "\x1fIMGSSH_PASTE\x1f"
)

func Run(ctx context.Context, args []string, stdin *os.File, stdout, stderr io.Writer) (int, error) {
	cfg, err := ParseArgs(args)
	if err != nil {
		return 2, err
	}
	if cfg.ShowHelp {
		fmt.Fprint(stdout, helpText())
		return 0, nil
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "imgssh %s\n", Version)
		return 0, nil
	}
	if len(cfg.SSHArgs) == 0 {
		return 2, errors.New("missing ssh arguments")
	}
	sshBin, err := exec.LookPath(cfg.SSHBin)
	if err != nil {
		return 127, fmt.Errorf("ssh binary not found: %s", cfg.SSHBin)
	}
	cfg.SSHBin = sshBin
	cfg.DebugInputPath = os.Getenv("IMGSSH_DEBUG_INPUT")

	parsed := ParseSSHArgs(cfg.SSHArgs)
	pasteEnabled := parsed.HasDestination && !parsed.TunnelOnly
	if !pasteEnabled && !cfg.Quiet {
		fmt.Fprintln(stderr, "imgssh: image paste disabled because ssh destination could not be parsed or tunnel-only mode was detected")
	}

	var controlDir string
	var controlPath string
	sessionArgs := append([]string(nil), cfg.SSHArgs...)
	connectArgs := append([]string(nil), parsed.ConnectArgs...)
	if pasteEnabled {
		controlDir, controlPath, err = newControlPath()
		if err != nil {
			return 1, err
		}
		defer os.RemoveAll(controlDir)
		controlOptions := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + controlPath, "-o", "ControlPersist=no"}
		sessionArgs = InsertSSHOptionsBeforeDestination(sessionArgs, controlOptions...)
	}

	cmd := exec.CommandContext(ctx, sshBin, sessionArgs...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, err
	}
	defer ptmx.Close()

	if term.IsTerminal(int(stdin.Fd())) {
		oldState, err := term.MakeRaw(int(stdin.Fd()))
		if err != nil {
			return 1, err
		}
		defer term.Restore(int(stdin.Fd()), oldState)
	}

	resizePTY(stdin, ptmx)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			resizePTY(stdin, ptmx)
		}
	}()

	doneCopy := make(chan struct{})
	go func() {
		io.Copy(stdout, ptmx)
		close(doneCopy)
	}()

	var uploading atomic.Bool
	go relayInput(ctx, stdin, ptmx, stderr, cfg, connectArgs, controlPath, pasteEnabled, &uploading)

	err = cmd.Wait()
	ptmx.Close()
	<-doneCopy
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// relayInput forwards local terminal input to the ssh pty while watching for a
// private sentinel trigger. It intentionally does not parse terminal protocols:
// Ghostty can bind Ctrl+] to send the sentinel, and every other byte is passed
// through unchanged.
func relayInput(ctx context.Context, stdin io.Reader, ptmx io.Writer, stderr io.Writer, cfg Config, connectArgs []string, controlPath string, pasteEnabled bool, uploading *atomic.Bool) {
	input := make(chan []byte, 16)
	go func() {
		defer close(input)
		var dbg *os.File
		if cfg.DebugInputPath != "" {
			if f, err := os.OpenFile(cfg.DebugInputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				dbg = f
				defer dbg.Close()
			}
		}
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			if dbg != nil && n > 0 {
				fmt.Fprintf(dbg, "%d IN %x\n", time.Now().UnixMilli(), buf[:n])
			}
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				input <- chunk
			}
			if err != nil {
				return
			}
		}
	}()

	triggerSeq := []byte(pasteTriggerSequence)
	var pending []byte
	emit := func(b []byte) {
		if len(b) > 0 {
			ptmx.Write(b)
		}
	}
	var normal []byte
	flushNormal := func() {
		emit(normal)
		normal = nil
	}
	flushPending := func() {
		emit(pending)
		pending = nil
	}
	trigger := func() {
		if pasteEnabled {
			startUpload(ctx, ptmx, stderr, cfg, connectArgs, controlPath, uploading)
		}
	}
	feed := func(b byte) {
		if len(pending) == 0 {
			if b == triggerSeq[0] {
				flushNormal()
				pending = []byte{b}
				return
			}
			normal = append(normal, b)
			return
		}

		pending = append(pending, b)
		switch {
		case bytes.Equal(pending, triggerSeq):
			pending = nil
			trigger()
		case bytes.HasPrefix(triggerSeq, pending):
			return
		default:
			keep := triggerPrefixSuffixLen(pending, triggerSeq)
			normal = append(normal, pending[:len(pending)-keep]...)
			if keep == 0 {
				pending = nil
			} else {
				kept := make([]byte, keep)
				copy(kept, pending[len(pending)-keep:])
				pending = kept
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			flushNormal()
			flushPending()
			return
		case chunk, ok := <-input:
			if !ok {
				flushNormal()
				flushPending()
				return
			}
			for _, b := range chunk {
				feed(b)
			}
			flushNormal()
		}
	}
}

func triggerPrefixSuffixLen(input, trigger []byte) int {
	max := len(input)
	if max >= len(trigger) {
		max = len(trigger) - 1
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(input[len(input)-n:], trigger[:n]) {
			return n
		}
	}
	return 0
}

func startUpload(ctx context.Context, ptmx io.Writer, stderr io.Writer, cfg Config, connectArgs []string, controlPath string, uploading *atomic.Bool) {
	if !uploading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer uploading.Store(false)
		png, err := ReadClipboardPNG(ctx, cfg.MaxSize)
		if err != nil {
			if !cfg.Quiet {
				fmt.Fprintf(stderr, "\r\nimgssh: %v\r\n", err)
			}
			return
		}
		path, err := UploadPNG(ctx, UploadPlan{
			SSHBin:      cfg.SSHBin,
			ConnectArgs: connectArgs,
			RemoteDir:   cfg.RemoteDir,
			MaxSize:     cfg.MaxSize,
			Timeout:     cfg.UploadTimeout,
			ControlPath: controlPath,
		}, png)
		if err != nil {
			if !cfg.Quiet {
				fmt.Fprintf(stderr, "\r\nimgssh: %v\r\n", err)
			}
			return
		}
		if !cfg.NoInject {
			ptmx.Write([]byte(ShellQuote(path)))
		}
	}()
}

func newControlPath() (string, string, error) {
	dir, err := os.MkdirTemp("", "imgssh-")
	if err != nil {
		return "", "", err
	}
	var b [4]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return dir, dir + "/cm-" + hex.EncodeToString(b[:]), nil
}

func resizePTY(stdin *os.File, ptmx *os.File) {
	if term.IsTerminal(int(stdin.Fd())) {
		_ = pty.InheritSize(stdin, ptmx)
	}
}

func helpText() string {
	return `Usage: imgssh [imgssh-options] [ssh-args...]

Options:
  --remote-dir <path>          remote upload directory (default: /tmp)
  --ssh-bin <path>             ssh binary path (default: ssh)
  --upload-timeout <duration>  upload timeout (default: 30s)
  --max-size <size>            max PNG size (default: 25MB)
  --no-inject                  upload without inserting the remote path
  --copy-path                  reserved
  --quiet                      suppress local failure messages
  --debug                      reserved
  --version                    print version
  --help                       print help

Environment:
  IMGSSH_DEBUG_INPUT=<file>    append a hex dump of received input (debugging)
`
}
