package imgssh

import (
	"bytes"
	"context"
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
