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
	"runtime"
	"strings"
	"time"
)

type UploadPlan struct {
	SSHBin      string
	ConnectArgs []string
	RemoteDir   string
	MaxSize     int64
	Timeout     time.Duration
	ControlPath string
}

func ReadClipboardPNG(ctx context.Context, maxSize int64) ([]byte, error) {
	cmd, err := clipboardCommand()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("clipboard image read failed: %s", msg)
		}
		return nil, errors.New("clipboard does not contain a readable PNG image")
	}
	if len(out) == 0 {
		return nil, errors.New("clipboard does not contain a PNG image")
	}
	if int64(len(out)) > maxSize {
		return nil, fmt.Errorf("clipboard image is too large: %d bytes exceeds %d bytes", len(out), maxSize)
	}
	return out, nil
}

func clipboardCommand() ([]string, error) {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("pngpaste"); err == nil {
			return []string{"pngpaste", "-"}, nil
		}
		return nil, errors.New("pngpaste is required to read PNG images from the clipboard")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return []string{"wl-paste", "--type", "image/png"}, nil
		}
	}
	if os.Getenv("DISPLAY") != "" {
		if _, err := exec.LookPath("xclip"); err == nil {
			return []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, nil
		}
		if _, err := exec.LookPath("xsel"); err == nil {
			return []string{"xsel", "--clipboard", "--output", "--mime-type", "image/png"}, nil
		}
	}
	return nil, errors.New("no supported PNG clipboard backend found")
}

func UploadPNG(ctx context.Context, plan UploadPlan, png []byte) (string, error) {
	remotePath, err := remotePath(plan.RemoteDir)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()

	args := append([]string{}, FilterUploadArgs(plan.ConnectArgs)...)
	options := []string{"-o", "BatchMode=yes"}
	if plan.ControlPath != "" {
		options = append(options, "-o", "ControlMaster=auto", "-o", "ControlPath="+plan.ControlPath)
	}
	args = InsertSSHOptionsBeforeDestination(args, options...)
	args = append(args, remoteUploadCommand(plan.RemoteDir, remotePath))
	cmd := exec.CommandContext(ctx, plan.SSHBin, args...)
	cmd.Stdin = bytes.NewReader(png)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("upload timed out after %s", plan.Timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("upload failed: %s", msg)
		}
		return "", fmt.Errorf("upload failed: %w", err)
	}
	return remotePath, nil
}

func remoteUploadCommand(remoteDir, remotePath string) string {
	parts := []string{
		"sh",
		"-c",
		`mkdir -p "$1" && cat > "$2"`,
		"sh",
		remoteDir,
		remotePath,
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, ShellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func remotePath(remoteDir string) (string, error) {
	if remoteDir == "" {
		return "", errors.New("remote dir cannot be empty")
	}
	var b [4]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf("imgssh-%s-%s.png", time.Now().Format("20060102-150405"), hex.EncodeToString(b[:]))
	if strings.HasSuffix(remoteDir, "/") {
		return remoteDir + name, nil
	}
	return remoteDir + "/" + name, nil
}
