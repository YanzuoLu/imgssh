package imgssh

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRelayInputForwardsCtrlGByDefault(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()

	relayInput(context.Background(), strings.NewReader("a\x07b"), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "a\x07b" {
		t.Fatalf("output = %q", got)
	}
}

func TestRelayInputConsumesCtrlRightBracketByDefault(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()

	relayInput(context.Background(), strings.NewReader("a\x1db"), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "ab" {
		t.Fatalf("output = %q", got)
	}
}

func TestRelayInputConsumesKittyCtrlRightBracket(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()

	relayInput(context.Background(), strings.NewReader("a\x1b[93;5ub"), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "ab" {
		t.Fatalf("output = %q", got)
	}
}

func TestRelayInputConsumesModifyOtherKeysCtrlRightBracket(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()

	relayInput(context.Background(), strings.NewReader("a\x1b[27;5;93~b"), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "ab" {
		t.Fatalf("output = %q", got)
	}
}

func TestRelayInputFlushesUnknownEscapeSequence(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()

	relayInput(context.Background(), strings.NewReader("a\x1b[Ab"), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "a\x1b[Ab" {
		t.Fatalf("output = %q", got)
	}
}

func TestRelayInputForwardsSGRMouse(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := "a\x1b[<64;30;10Mb"

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("output = %q, want %q", got, in)
	}
}

func TestRelayInputForwardsMouseBurstByteFaithful(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := strings.Repeat("\x1b[<64;30;10M\x1b[<65;31;11M", 200)

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("burst corrupted: got len=%d want len=%d", len(got), len(in))
	}
}

// oneByteReader yields a single byte per Read — the worst-case chunking — to
// prove the parser never depends on an escape sequence arriving in one read.
type oneByteReader struct {
	data []byte
	i    int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.i]
	r.i++
	return 1, nil
}

func TestRelayInputByteFaithfulAcrossChunks(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	// Text, SGR mouse, arrow keys, a consumed trigger, then more mouse.
	in := "hi\x1b[<64;30;10M\x1b[A\x1b[B\x1b[93;5ux\x1b[<35;1;1m"
	want := "hi\x1b[<64;30;10M\x1b[A\x1b[Bx\x1b[<35;1;1m" // trigger removed

	relayInput(context.Background(), &oneByteReader{data: []byte(in)}, &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRelayInputForwardsNearMissCSIu(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	// Modifier 6, not 5: not the trigger, must pass through untouched.
	in := "a\x1b[93;6ub"

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("output = %q, want %q", got, in)
	}
}
