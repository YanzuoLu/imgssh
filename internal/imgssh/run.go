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

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
	x10MousePrefix      = []byte("\x1b[M")
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

// relayInput forwards local terminal input to the ssh pty while watching for
// the paste trigger (raw Ctrl+] = 0x1d, or its CSI-u encodings). It parses
// escape sequences as whole units so that mouse, arrow and bracketed-paste
// sequences are always forwarded atomically and never split mid-sequence;
// splitting them is what corrupted tmux mouse/scroll input before.
func relayInput(ctx context.Context, stdin io.Reader, ptmx io.Writer, stderr io.Writer, cfg Config, connectArgs []string, controlPath string, pasteEnabled bool, uploading *atomic.Bool) {
	const partialTimeout = 75 * time.Millisecond

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

	const (
		stNormal          = iota
		stEsc             // saw ESC, awaiting the next byte
		stCSI             // inside an ESC '[' control sequence
		stSS2SS3          // inside an ESC 'N'/'O' single-shift sequence
		stEscIntermediate // inside an ESC sequence with intermediate bytes
		stX10Mouse        // inside an ESC '[' 'M' mouse event payload
		stBracketedPaste  // inside bracketed paste content
		stString          // inside an OSC/DCS/APC/PM/SOS string sequence
	)
	state := stNormal
	var pending []byte
	var x10MouseBytesRemaining int
	var stringIntro byte
	var stringSawEsc bool
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
	var normal []byte
	flushNormal := func() {
		emit(normal)
		normal = nil
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
			flushNormal()
			flushPending()
			return
		case <-timeout:
			// A partial escape sequence stalled (most often a bare Esc key
			// press). Forward whatever we buffered, intact, and resync.
			switch state {
			case stBracketedPaste:
				if len(pending) > 0 && bytes.HasPrefix(bracketedPasteEnd, pending) {
					stopTimer()
				} else {
					flushPending()
				}
				// Stay inside payload modes: their contents may arrive slowly,
				// and returning to normal would re-enable local trigger parsing.
			case stString:
				flushPending()
				// Stay inside payload modes: their contents may arrive slowly,
				// and returning to normal would re-enable local trigger parsing.
			case stX10Mouse:
				stopTimer()
			default:
				flushPending()
				state = stNormal
			}
		case chunk, ok := <-input:
			if !ok {
				flushNormal()
				flushPending()
				return
			}
			for _, b := range chunk {
				switch state {
				case stNormal:
					switch {
					case b == pasteTrigger:
						flushNormal()
						trigger()
					case b == escByte:
						flushNormal()
						pending = []byte{b}
						state = stEsc
						armTimer()
					default:
						normal = append(normal, b)
					}
				case stEsc:
					stopTimer()
					switch b {
					case '[':
						pending = append(pending, b)
						state = stCSI
						armTimer()
					case 'N', 'O':
						pending = append(pending, b)
						state = stSS2SS3
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
						stringIntro = b
						stringSawEsc = false
						state = stString
						armTimer()
					default:
						if b >= 0x20 && b <= 0x2f {
							pending = append(pending, b)
							state = stEscIntermediate
							armTimer()
						} else {
							// Two-byte ESC sequence (Alt-key, ...): never a
							// paste trigger, forward both bytes untouched.
							pending = append(pending, b)
							flushPending()
							state = stNormal
						}
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
						} else if bytes.Equal(pending, bracketedPasteStart) {
							flushPending()
							state = stBracketedPaste
						} else if bytes.Equal(pending, x10MousePrefix) {
							x10MouseBytesRemaining = 3
							state = stX10Mouse
							armTimer()
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
				case stSS2SS3:
					stopTimer()
					pending = append(pending, b)
					switch {
					case b >= 0x40 && b <= 0x7e:
						flushPending()
						state = stNormal
					case len(pending) >= maxEscBuffer:
						flushPending()
						state = stNormal
					default:
						armTimer()
					}
				case stEscIntermediate:
					stopTimer()
					pending = append(pending, b)
					switch {
					case b >= 0x30 && b <= 0x7e:
						flushPending()
						state = stNormal
					case len(pending) >= maxEscBuffer:
						flushPending()
						state = stNormal
					default:
						armTimer()
					}
				case stX10Mouse:
					stopTimer()
					pending = append(pending, b)
					x10MouseBytesRemaining--
					if x10MouseBytesRemaining <= 0 {
						flushPending()
						state = stNormal
					} else {
						armTimer()
					}
				case stBracketedPaste:
					if len(pending) == 0 {
						if b == escByte {
							flushNormal()
							pending = []byte{b}
							armTimer()
						} else {
							normal = append(normal, b)
						}
						break
					}

					stopTimer()
					pending = append(pending, b)
					switch {
					case bytes.Equal(pending, bracketedPasteEnd):
						flushPending()
						state = stNormal
					case bytes.HasPrefix(bracketedPasteEnd, pending):
						armTimer()
					default:
						if b == escByte {
							emit(pending[:len(pending)-1])
							pending = []byte{b}
							armTimer()
						} else {
							flushPending()
						}
					}
				case stString:
					stopTimer()
					pending = append(pending, b)
					switch {
					case stringIntro == ']' && b == 0x07:
						// BEL terminates OSC. Other string controls use ST.
						flushPending()
						state = stNormal
						stringIntro = 0
						stringSawEsc = false
					case stringSawEsc && b == '\\':
						// ST terminator (ESC \).
						flushPending()
						state = stNormal
						stringIntro = 0
						stringSawEsc = false
					case len(pending) >= maxStringBuffer:
						// Long string payload: forward what we have but stay in
						// string mode so payload bytes cannot trigger local actions.
						flushPending()
						stringSawEsc = b == escByte
						armTimer()
					default:
						stringSawEsc = b == escByte
						armTimer()
					}
				}
			}
			if state == stNormal || (state == stBracketedPaste && len(pending) == 0) {
				flushNormal()
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
