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

const pasteTrigger = byte(0x1d)

var pasteTriggerSequences = [][]byte{
	[]byte("\x1b[93;5u"),
	[]byte("\x1b[27;5;93~"),
}

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

func relayInput(ctx context.Context, stdin io.Reader, ptmx io.Writer, stderr io.Writer, cfg Config, connectArgs []string, controlPath string, pasteEnabled bool, uploading *atomic.Bool) {
	const partialTimeout = 75 * time.Millisecond

	input := make(chan byte, 4096)
	go func() {
		defer close(input)
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			for _, b := range buf[:n] {
				input <- b
			}
			if err != nil {
				return
			}
		}
	}()

	var pending []byte
	var timer *time.Timer
	var timeout <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timeout = nil
		}
	}
	resetTimer := func() {
		stopTimer()
		timer = time.NewTimer(partialTimeout)
		timeout = timer.C
	}
	flushPending := func() {
		if len(pending) > 0 {
			ptmx.Write(pending)
			pending = nil
		}
		stopTimer()
	}
	trigger := func() {
		if pasteEnabled {
			startUpload(ctx, ptmx, stderr, cfg, connectArgs, controlPath, uploading)
		}
	}

	for {
		select {
		case <-ctx.Done():
			flushPending()
			return
		case <-timeout:
			flushPending()
		case b, ok := <-input:
			if !ok {
				flushPending()
				return
			}
			if b == pasteTrigger {
				flushPending()
				trigger()
				continue
			}
			if len(pending) > 0 || startsPasteTriggerSequence(b) {
				pending = append(pending, b)
				if matchesPasteTriggerSequence(pending) {
					pending = nil
					stopTimer()
					trigger()
					continue
				}
				if prefixesPasteTriggerSequence(pending) {
					resetTimer()
					continue
				}
				flushPending()
				continue
			}
			ptmx.Write([]byte{b})
		}
	}
}

func startsPasteTriggerSequence(b byte) bool {
	for _, seq := range pasteTriggerSequences {
		if len(seq) > 0 && seq[0] == b {
			return true
		}
	}
	return false
}

func matchesPasteTriggerSequence(input []byte) bool {
	for _, seq := range pasteTriggerSequences {
		if bytes.Equal(input, seq) {
			return true
		}
	}
	return false
}

func prefixesPasteTriggerSequence(input []byte) bool {
	for _, seq := range pasteTriggerSequences {
		if bytes.HasPrefix(seq, input) {
			return true
		}
	}
	return false
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
`
}
