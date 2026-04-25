package imgssh

import "testing"

func TestDefaultConfigUsesCtrlRightBracket(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.RemoteDir != "/tmp" {
		t.Fatalf("RemoteDir = %q", cfg.RemoteDir)
	}
}

func TestParseArgsSeparatesImgsshOptionsFromSSHArgs(t *testing.T) {
	cfg, err := ParseArgs([]string{"--remote-dir", "/var/tmp", "--ssh-bin=/usr/bin/ssh", "-J", "bastion", "coder@dev", "tmux", "attach"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteDir != "/var/tmp" {
		t.Fatalf("RemoteDir = %q", cfg.RemoteDir)
	}
	if cfg.SSHBin != "/usr/bin/ssh" {
		t.Fatalf("SSHBin = %q", cfg.SSHBin)
	}
	want := []string{"-J", "bastion", "coder@dev", "tmux", "attach"}
	assertStrings(t, cfg.SSHArgs, want)
}

func TestParseArgsDoubleDash(t *testing.T) {
	cfg, err := ParseArgs([]string{"--remote-dir", "/tmp", "--", "-J", "bastion", "coder@dev"})
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, cfg.SSHArgs, []string{"-J", "bastion", "coder@dev"})
}

func TestParseSize(t *testing.T) {
	tests := map[string]int64{
		"25MB": 25 * 1024 * 1024,
		"1KB":  1024,
		"9B":   9,
		"7":    7,
	}
	for input, want := range tests {
		got, err := ParseSize(input)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseSize(%q) = %d, want %d", input, got, want)
		}
	}
}
