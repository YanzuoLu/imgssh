package imgssh

import "testing"

func TestParseSSHArgsProxyJumpAndRemoteCommand(t *testing.T) {
	res := ParseSSHArgs([]string{"-J", "bastion", "-p", "2222", "coder@dev", "tmux", "attach"})
	if !res.HasDestination {
		t.Fatal("expected destination")
	}
	assertStrings(t, res.ConnectArgs, []string{"-J", "bastion", "-p", "2222", "coder@dev"})
	assertStrings(t, res.RemoteCommand, []string{"tmux", "attach"})
}

func TestParseSSHArgsInlineOptionValue(t *testing.T) {
	res := ParseSSHArgs([]string{"-p2222", "-i/home/me/.ssh/id_ed25519", "coder@dev"})
	if !res.HasDestination {
		t.Fatal("expected destination")
	}
	assertStrings(t, res.ConnectArgs, []string{"-p2222", "-i/home/me/.ssh/id_ed25519", "coder@dev"})
}

func TestFilterUploadArgsRemovesTTYAllocation(t *testing.T) {
	got := FilterUploadArgs([]string{"-J", "bastion", "-tt", "-p", "2222", "-t", "coder@dev"})
	assertStrings(t, got, []string{"-J", "bastion", "-p", "2222", "coder@dev"})
}

func TestInsertSSHOptionsBeforeDestination(t *testing.T) {
	got := InsertSSHOptionsBeforeDestination(
		[]string{"-J", "bastion", "-p", "2222", "coder@dev", "tmux", "attach"},
		"-o", "ControlPath=/tmp/cm",
	)
	assertStrings(t, got, []string{"-J", "bastion", "-p", "2222", "-o", "ControlPath=/tmp/cm", "coder@dev", "tmux", "attach"})
}

func TestParseSSHArgsTunnelOnly(t *testing.T) {
	res := ParseSSHArgs([]string{"-N", "coder@dev"})
	if !res.TunnelOnly {
		t.Fatal("expected tunnel-only mode")
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %#v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("index %d = %q, want %q; got %#v", i, got[i], want[i], got)
		}
	}
}
