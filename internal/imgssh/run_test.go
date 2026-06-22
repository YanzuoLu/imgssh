package imgssh

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

var testPasteTriggerSequence = []byte("\x1fIMGSSH_PASTE\x1f")

func TestRelayInputConsumesSentinelTrigger(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := "a" + string(testPasteTriggerSequence) + "b"

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "ab" {
		t.Fatalf("output = %q, want %q", got, "ab")
	}
}

func TestRelayInputConsumesSentinelAcrossChunks(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := []byte("a" + string(testPasteTriggerSequence) + "b")

	relayInput(context.Background(), &oneByteReader{data: in}, &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != "ab" {
		t.Fatalf("output = %q, want %q", got, "ab")
	}
}

func TestRelayInputForwardsRawCtrlRightBracket(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := "a\x1db"

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("output = %q, want %q", got, in)
	}
}

func TestRelayInputForwardsControlAndTerminalSequences(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := strings.Join([]string{
		"\x1bOC",                    // SS3 application cursor right
		"\x1b[47;47R",               // cursor position report
		"\x1b]11;rgb:1111/2222\x07", // OSC reply
		"\x1bPpayload\x1b\\",        // DCS string
		"\x1b[200~raw \x1d text\x1b[201~",
		"中文输入不会吞字",
	}, "")

	relayInput(context.Background(), strings.NewReader(in), &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("output = %q, want %q", got, in)
	}
}

func TestRelayInputForwardsPartialSentinelOnMismatch(t *testing.T) {
	var out bytes.Buffer
	var uploading atomic.Bool
	cfg := DefaultConfig()
	in := "a\x1fIMGSSH_NOPE\x1fIMGSSH_PASTE_Xb"

	relayInput(context.Background(), &oneByteReader{data: []byte(in)}, &out, &bytes.Buffer{}, cfg, nil, "", false, &uploading)

	if got := out.String(); got != in {
		t.Fatalf("output = %q, want %q", got, in)
	}
}

// oneByteReader yields a single byte per Read to prove trigger matching does
// not depend on the sentinel arriving in one read.
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
