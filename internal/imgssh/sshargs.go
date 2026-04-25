package imgssh

import "strings"

type SSHParseResult struct {
	SessionArgs      []string
	ConnectArgs      []string
	RemoteCommand    []string
	HasDestination   bool
	DestinationIndex int
	TunnelOnly       bool
}

var sshOptionsWithValue = map[byte]bool{
	'B': true, 'b': true, 'c': true, 'D': true, 'E': true, 'e': true,
	'F': true, 'I': true, 'i': true, 'J': true, 'L': true, 'l': true,
	'm': true, 'O': true, 'o': true, 'p': true, 'Q': true, 'R': true,
	'S': true, 'W': true, 'w': true,
}

func ParseSSHArgs(args []string) SSHParseResult {
	res := SSHParseResult{SessionArgs: append([]string(nil), args...), DestinationIndex: -1}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				res.ConnectArgs = append([]string(nil), args[:i+2]...)
				res.RemoteCommand = append([]string(nil), args[i+2:]...)
				res.HasDestination = true
				res.DestinationIndex = i + 1
			}
			return res
		}
		if arg == "-N" || arg == "-nN" || arg == "-Nn" || strings.Contains(arg, "W") && strings.HasPrefix(arg, "-") {
			res.TunnelOnly = true
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			res.ConnectArgs = append([]string(nil), args[:i+1]...)
			res.RemoteCommand = append([]string(nil), args[i+1:]...)
			res.HasDestination = true
			res.DestinationIndex = i
			return res
		}
		if skipNextSSHOptionArg(arg) && !shortOptionHasInlineValue(arg) && i+1 < len(args) {
			i++
		}
	}
	return res
}

func InsertSSHOptionsBeforeDestination(args []string, options ...string) []string {
	parsed := ParseSSHArgs(args)
	if !parsed.HasDestination {
		out := append([]string(nil), args...)
		return append(out, options...)
	}
	out := make([]string, 0, len(args)+len(options))
	out = append(out, args[:parsed.DestinationIndex]...)
	out = append(out, options...)
	out = append(out, args[parsed.DestinationIndex:]...)
	return out
}

func skipNextSSHOptionArg(arg string) bool {
	if len(arg) < 2 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	opt := arg[1:]
	if len(opt) == 0 {
		return false
	}
	return len(opt) == 1 && sshOptionsWithValue[opt[0]]
}

func shortOptionHasInlineValue(arg string) bool {
	if len(arg) < 3 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	opt := arg[1]
	return sshOptionsWithValue[opt]
}

func FilterUploadArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-t" || arg == "-tt" {
			continue
		}
		out = append(out, arg)
	}
	return out
}
