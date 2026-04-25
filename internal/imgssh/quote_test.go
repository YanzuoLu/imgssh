package imgssh

import "testing"

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"":        "''",
		"/tmp/a":  "'/tmp/a'",
		"abc'def": "'abc'\\''def'",
	}
	for input, want := range tests {
		if got := ShellQuote(input); got != want {
			t.Fatalf("ShellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}
