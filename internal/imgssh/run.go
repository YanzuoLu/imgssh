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
	pasteTrigger    = byte(0x1d)
	escByte         = byte(0x1b)
	maxEscBuffer    = 64
	maxStringBuffer = 8192
)

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

// relayInput forwards local terminal input to the ssh pty while watching for
// the paste trigger (raw Ctrl+] = 0x1d, or its CSI-u encodings). It parses
// escape sequences as whole units so that mouse, arrow and bracketed-paste
// sequences are always forwarded atomically and never split mid-sequence;
// splitting them is what corrupted tmux mouse/scroll input before.
func relayInput(ctx context.Context, stdin io.Reader, ptmx io.Writer, stderr io.Writer, cfg Config, connectArgs []string, controlPath string, pasteEnabled bool, uploading *atomic.Bool) {
	const partialTimeout = 75 * time.Millisecond

	input := make(chan byte, 4096)
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
			for _, b := range buf[:n] {
				input <- b
			}
			if err != nil {
				return
			}
		}
	}()

	const (
		stNormal = iota
		stEsc    // saw ESC, awaiting the next byte
		stCSI    // inside an ESC '[' control sequence
		stString // inside an OSC/DCS/APC/PM/SOS string sequence
	)
	state := stNormal
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
	armTimer := func() {
		stopTimer()
		timer = time.NewTimer(partialTimeout)
		timeout = timer.C
	}
	emit := func(b []byte) {
		if len(b) > 0 {
			ptmx.Write(b)
		}
	}
	flushPending := func() {
		emit(pending)
		pending = nil
		stopTimer()
	}
	trigger := func() {
		stopTimer()
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
			// A partial escape sequence stalled (most often a bare Esc key
			// press). Forward whatever we buffered, intact, and resync.
			flushPending()
			state = stNormal
		case b, ok := <-input:
			if !ok {
				flushPending()
				return
			}
			switch state {
			case stNormal:
				switch {
				case b == pasteTrigger:
					trigger()
				case b == escByte:
					pending = []byte{b}
					state = stEsc
					armTimer()
				default:
					emit([]byte{b})
				}
			case stEsc:
				stopTimer()
				switch b {
				case '[':
					pending = append(pending, b)
					state = stCSI
					armTimer()
				case escByte:
					// ESC ESC: flush the first, start over on the second.
					emit(pending)
					pending = []byte{b}
					state = stEsc
					armTimer()
				case pasteTrigger:
					emit(pending)
					pending = nil
					state = stNormal
					trigger()
				case ']', 'P', '_', '^', 'X':
					// OSC/DCS/APC/PM/SOS: a string sequence whose payload runs
					// until BEL or ST. Buffer it so the whole reply (e.g. an
					// OSC 11 "rgb:" colour response) reaches the app in one
					// write; dribbling it byte-by-byte made apps drop it over
					// ssh and leak the tail onto the screen.
					pending = append(pending, b)
					state = stString
					armTimer()
				default:
					// Two-byte ESC sequence (Alt-key, SS3 intro, ...): never a
					// paste trigger, forward both bytes untouched.
					pending = append(pending, b)
					flushPending()
					state = stNormal
				}
			case stCSI:
				stopTimer()
				pending = append(pending, b)
				switch {
				case b >= 0x40 && b <= 0x7e:
					// CSI final byte: the control sequence is complete.
					if matchesPasteTriggerSequence(pending) {
						pending = nil
						state = stNormal
						trigger()
					} else {
						flushPending()
						state = stNormal
					}
				case len(pending) >= maxEscBuffer:
					// Unterminated/runaway: forward what we have and resync.
					flushPending()
					state = stNormal
				default:
					armTimer()
				}
			case stString:
				stopTimer()
				pending = append(pending, b)
				n := len(pending)
				switch {
				case b == 0x07:
					// BEL terminator.
					flushPending()
					state = stNormal
				case n >= 2 && pending[n-2] == escByte && b == '\\':
					// ST terminator (ESC \).
					flushPending()
					state = stNormal
				case n >= maxStringBuffer:
					// Runaway/unterminated: forward what we have and resync.
					flushPending()
					state = stNormal
				default:
					armTimer()
				}
			}
		}
	}
}

func matchesPasteTriggerSequence(input []byte) bool {
	for _, seq := range pasteTriggerSequences {
		if bytes.Equal(input, seq) {
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

Environment:
  IMGSSH_DEBUG_INPUT=<file>    append a hex dump of received input (debugging)
`
}
