package imgssh

import (
	"strings"
	"testing"
)

func TestRemotePath(t *testing.T) {
	path, err := remotePath("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/tmp/imgssh-") || !strings.HasSuffix(path, ".png") {
		t.Fatalf("unexpected path %q", path)
	}

	path, err = remotePath("/tmp/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "//") {
		t.Fatalf("unexpected doubled slash in %q", path)
	}
}

func TestRemoteUploadCommandQuotesPositionalArgs(t *testing.T) {
	got := remoteUploadCommand("/tmp/with space", "/tmp/with space/a'b.png")
	want := `'sh' '-c' 'mkdir -p "$1" && cat > "$2"' 'sh' '/tmp/with space' '/tmp/with space/a'\''b.png'`
	if got != want {
		t.Fatalf("remoteUploadCommand() = %q, want %q", got, want)
	}
}
