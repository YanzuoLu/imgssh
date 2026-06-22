package imgssh

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RemoteDir     string
	SSHBin        string
	UploadTimeout time.Duration
	MaxSize       int64
	NoInject      bool
	CopyPath      bool
	Quiet         bool
	Debug         bool
	ShowHelp      bool
	ShowVersion   bool
	SSHArgs       []string

	DebugInputPath string
}

const Version = "0.1.4"

func DefaultConfig() Config {
	sshBin := os.Getenv("IMGSSH_SSH_BIN")
	if sshBin == "" {
		sshBin = "ssh"
	}
	return Config{
		RemoteDir:     "/tmp",
		SSHBin:        sshBin,
		UploadTimeout: 30 * time.Second,
		MaxSize:       25 * 1024 * 1024,
	}
}

func ParseArgs(args []string) (Config, error) {
	cfg := DefaultConfig()
	var sshArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			sshArgs = append(sshArgs, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			sshArgs = append(sshArgs, args[i:]...)
			break
		}

		name, value, hasValue := strings.Cut(arg, "=")
		takeValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			i++
			return args[i], nil
		}

		switch name {
		case "--remote-dir":
			v, err := takeValue()
			if err != nil {
				return cfg, err
			}
			cfg.RemoteDir = v
		case "--ssh-bin":
			v, err := takeValue()
			if err != nil {
				return cfg, err
			}
			cfg.SSHBin = v
		case "--upload-timeout":
			v, err := takeValue()
			if err != nil {
				return cfg, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return cfg, fmt.Errorf("invalid upload timeout: %w", err)
			}
			cfg.UploadTimeout = d
		case "--max-size":
			v, err := takeValue()
			if err != nil {
				return cfg, err
			}
			n, err := ParseSize(v)
			if err != nil {
				return cfg, err
			}
			cfg.MaxSize = n
		case "--no-inject":
			cfg.NoInject = true
		case "--copy-path":
			cfg.CopyPath = true
		case "--quiet":
			cfg.Quiet = true
		case "--debug":
			cfg.Debug = true
		case "--version":
			cfg.ShowVersion = true
		case "--help", "-h":
			cfg.ShowHelp = true
		default:
			sshArgs = append(sshArgs, args[i:]...)
			i = len(args)
		}
	}

	cfg.SSHArgs = sshArgs
	return cfg, nil
}

func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("size cannot be empty")
	}
	upper := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, suffix := range []struct {
		name string
		mult int64
	}{
		{"KB", 1024},
		{"MB", 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, suffix.name) {
			upper = strings.TrimSpace(strings.TrimSuffix(upper, suffix.name))
			mult = suffix.mult
			break
		}
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
