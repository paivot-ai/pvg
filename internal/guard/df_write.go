package guard

// Write-intent detection for Bash commands that touch D&F artifacts.
//
// The D&F guard must fire on a WRITE to a resolved artifact PATH, never on the
// artifact NAME appearing somewhere in the command text. Two shapes are
// legitimate and must pass:
//
//	awk '/x/' design/BUILD.md; grep -n y design/ARCHITECTURE.md   (pure read)
//	cat > design/DECISIONS.md <<'EOF' ... mentions BUSINESS.md ... (other file)
//
// So the command is parsed instead of scanned: heredoc bodies are dropped
// (their content is data, not a target), redirect targets are read with the
// same reader the vault guards use, and a small set of write utilities
// contributes the operands they actually write.
//
// Known limitation (pre-existing): interpreter writes -- python3 -c
// "open('ARCHITECTURE.md','w')" -- are opaque to this parser and are not
// detected here, exactly as before.

import (
	"path/filepath"
	"regexp"
	"strings"
)

// heredocRe matches a heredoc introducer (<<WORD, <<-WORD, <<'WORD', <<"WORD")
// and captures the delimiter. The leading (?:^|[^<]) keeps a here-string
// (<<<WORD) from being mistaken for a heredoc.
var heredocRe = regexp.MustCompile(`(?:^|[^<])<<-?[ \t]*(?:'([^']*)'|"([^"]*)"|([A-Za-z_][A-Za-z0-9_]*))`)

// bashWriteTargets returns the paths command appears to write to: redirect
// targets plus the written operands of common write utilities. Reads
// (grep/awk/sed -n/cat/head/tail/less) contribute nothing.
func bashWriteTargets(command string) []string {
	stripped := stripHeredocBodies(command)

	targets := redirectTargets(stripped)
	for _, segment := range shellSegments(stripped) {
		targets = append(targets, segmentWriteTargets(segment)...)
	}
	return targets
}

// stripHeredocBodies removes the body of every heredoc in command. The body is
// content being written, not a set of paths: a decision record that mentions
// "BUSINESS.md" must not read as a write to BUSINESS.md.
func stripHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		kept = append(kept, lines[i])
		delim := heredocDelimiter(lines[i])
		if delim == "" {
			continue
		}
		for i+1 < len(lines) {
			i++
			if strings.TrimSpace(lines[i]) == delim {
				break
			}
		}
	}
	return strings.Join(kept, "\n")
}

// heredocDelimiter returns the heredoc delimiter introduced on line, or "".
func heredocDelimiter(line string) string {
	m := heredocRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	for _, group := range m[1:] {
		if group != "" {
			return group
		}
	}
	return ""
}

// shellSegments splits command into simple commands (breaking at unquoted
// ; | & && || newline and subshell parens) and returns each as its tokens,
// unquoted. Redirect operators and their target tokens are dropped: targets
// are collected separately by redirectTargets, and leaving them in would make
// them look like operands of the utility being run.
func shellSegments(command string) [][]string {
	var (
		segments [][]string
		current  []string
		token    strings.Builder
		hasToken bool
		dropNext bool
	)

	flush := func() {
		if !hasToken {
			return
		}
		word := token.String()
		token.Reset()
		hasToken = false
		if dropNext {
			dropNext = false
			return
		}
		current = append(current, word)
	}
	endSegment := func() {
		flush()
		if len(current) > 0 {
			segments = append(segments, current)
			current = nil
		}
		dropNext = false
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]
		switch {
		case ch == '\'' || ch == '"':
			hasToken = true
			j := i + 1
			for j < len(command) && command[j] != ch {
				token.WriteByte(command[j])
				j++
			}
			i = j
		case ch == '\\' && i+1 < len(command):
			hasToken = true
			token.WriteByte(command[i+1])
			i++
		case ch == ' ' || ch == '\t':
			flush()
		case ch == '\n' || ch == ';' || ch == '|' || ch == '&' || ch == '(' || ch == ')':
			endSegment()
		case ch == '<' || ch == '>':
			flush()
			i = skipRedirectOperator(command, i, &dropNext)
		default:
			hasToken = true
			token.WriteByte(ch)
		}
	}
	endSegment()
	return segments
}

// skipRedirectOperator consumes the redirection operator starting at i and
// reports (via dropNext) whether the following word is its path target. It
// returns the index of the operator's last byte. Fd duplication (2>&1, >&2)
// has no path target.
func skipRedirectOperator(command string, i int, dropNext *bool) int {
	j := i + 1
	if j < len(command) && command[j] == command[i] { // >> or <<
		j++
	}
	if j < len(command) && command[j] == '|' { // >| clobber
		j++
	}
	if j < len(command) && command[j] == '&' {
		k := j + 1
		for k < len(command) && command[k] >= '0' && command[k] <= '9' {
			k++
		}
		if k > j+1 { // >&N: fd duplication, no path follows
			return k - 1
		}
		j++ // `>& file`: a path does follow
	}
	*dropNext = true
	return j - 1
}

// inPlaceFlags mark an editor invocation that rewrites its file operands.
var inPlaceFlags = []string{"-i", "--in-place", "-pi", "-ni"}

// segmentWriteTargets returns the operands a simple command writes to.
func segmentWriteTargets(tokens []string) []string {
	name, args := commandAndArgs(tokens)
	operands := nonFlagArgs(args)

	switch name {
	case "tee", "truncate", "patch":
		return operands
	case "mv", "rm":
		// mv and rm destroy their source operands, so every operand counts.
		return operands
	case "cp", "install", "rsync", "ln":
		if len(operands) >= 2 {
			return operands[len(operands)-1:]
		}
	case "dd":
		for _, arg := range args {
			if strings.HasPrefix(arg, "of=") {
				return []string{strings.TrimPrefix(arg, "of=")}
			}
		}
	case "sed", "gsed", "perl":
		if hasInPlaceFlag(args) {
			// The script operand ("s/a/b/") is never an artifact path, so
			// returning every operand costs nothing and catches the files.
			return operands
		}
	}
	return nil
}

// commandAndArgs resolves the utility a segment runs, skipping leading
// environment assignments and common wrappers, and returns its base name plus
// the remaining arguments.
func commandAndArgs(tokens []string) (string, []string) {
	for i, tok := range tokens {
		switch {
		case tok == "":
			continue
		case isEnvAssignment(tok):
			continue
		case tok == "sudo" || tok == "command" || tok == "env" || tok == "time" || tok == "nohup":
			continue
		default:
			return filepath.Base(tok), tokens[i+1:]
		}
	}
	return "", nil
}

func isEnvAssignment(token string) bool {
	idx := strings.Index(token, "=")
	if idx <= 0 {
		return false
	}
	for i := 0; i < idx; i++ {
		ch := token[i]
		if ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

func nonFlagArgs(args []string) []string {
	var operands []string
	for _, arg := range args {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		operands = append(operands, arg)
	}
	return operands
}

func hasInPlaceFlag(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		for _, flag := range inPlaceFlags {
			if arg == flag || strings.HasPrefix(arg, flag+".") || strings.HasPrefix(arg, flag+"'") {
				return true
			}
		}
		// Clustered short flags (perl -pi, sed -ri): an 'i' among them.
		if !strings.HasPrefix(arg, "--") && strings.Contains(arg, "i") {
			return true
		}
	}
	return false
}
