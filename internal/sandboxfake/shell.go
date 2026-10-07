package sandboxfake

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// This file interprets the `sh -c` subset that sandboxWorkspace emits over the
// sandbox Exec transport. Everything the workspace does in-band goes through
// exactly these forms:
//
//	t=$(mktemp -- "$1.XXXXXX") || exit 1   # assignment + command substitution
//	trap 'rm -f -- "$t"' EXIT              # EXIT trap, expanded when run
//	cat > "$t" || exit 1                   # stdout redirection
//	chmod "$2" "$t" || exit 1
//	mv -f -- "$t" "$1"
//	[ -e "$1" ] || [ -L "$1" ] || exit 42  # test, || chains
//	stat -c "$2" -- "$1"                   # GNU stat with a format string
//	stat -L -c "$2" -- "$1"
//	rm -f -- "$1"
//	find "$1" -mindepth 1 -maxdepth 1 -printf '%y\0%f\0'
//	mkdir -p -- "$1" && chmod "$2" "$1"    # && chains
//
// Anything outside this subset fails with a diagnostic rather than a wrong
// answer, so a workspace change that grows the shell surface is caught by the
// tests instead of silently succeeding. Tests needing arbitrary commands
// (go build, git, rg) register ExecHook.

// shSession is one `sh -c` invocation.
type shSession struct {
	sb     *Sandbox
	ctx    context.Context
	cwd    string
	stdin  []byte
	params []string // $0, $1, ...: sh -c script name arg1...
	vars   map[string]string
	trap   string // EXIT trap body, expanded when it runs
	exited bool
	stdout strings.Builder
	stderr strings.Builder
}

var (
	assignSubst = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=\$\((.*)\)$`)
	assignPlain = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
)

// runSh interprets a `sh -c` request. Callers must not hold s.mu; the whole
// script runs under it, which keeps the fake's tree consistent for the
// duration and is fine because scripts are short.
func (s *Sandbox) runSh(ctx context.Context, req shepherd.ExecRequest) shepherd.ExecResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := &shSession{
		sb:     s,
		ctx:    ctx,
		cwd:    req.Cwd,
		stdin:  req.Stdin,
		params: req.Args[2:],
		vars:   map[string]string{},
	}
	if sh.cwd == "" {
		sh.cwd = "/"
	}
	code := sh.run(req.Args[1])
	return shepherd.ExecResult{
		ExitCode: code,
		Stdout:   sh.stdout.String(),
		Stderr:   sh.stderr.String(),
	}
}

// run executes the script line by line, then the EXIT trap if one was set.
func (sh *shSession) run(script string) int {
	code := 0
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code = sh.runLine(line)
		if sh.exited {
			break
		}
	}
	if sh.trap != "" {
		sh.exited = true // an exit inside the trap cannot re-exit
		sh.runLine(sh.trap)
	}
	return code
}

// segment is one command of an &&/|| chain.
type segment struct {
	op   string // "", "&&", "||"
	text string
}

// splitAndOr splits a line on && and || outside quotes.
func splitAndOr(line string) []segment {
	var segs []segment
	var cur strings.Builder
	op := ""
	inS, inD := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\'' && !inD {
			inS = !inS
		} else if c == '"' && !inS {
			inD = !inD
		}
		if !inS && !inD && (c == '&' || c == '|') && i+1 < len(line) && line[i+1] == c {
			segs = append(segs, segment{op: op, text: strings.TrimSpace(cur.String())})
			cur.Reset()
			op = string(c) + string(c)
			i++
			continue
		}
		cur.WriteByte(c)
	}
	return append(segs, segment{op: op, text: strings.TrimSpace(cur.String())})
}

func (sh *shSession) runLine(line string) int {
	segs := splitAndOr(line)
	code := 0
	for i, seg := range segs {
		if i > 0 {
			if seg.op == "&&" && code != 0 {
				return code
			}
			if seg.op == "||" && code == 0 {
				return code
			}
		}
		code = sh.runSegment(seg.text)
		if sh.exited {
			return code
		}
	}
	return code
}

func (sh *shSession) runSegment(text string) int {
	if text == "" {
		return 0
	}

	// Assignments: t=$(cmd ...) captures the command's stdout (one trailing
	// newline stripped, per sh); the assignment's status is the command's.
	if m := assignSubst.FindStringSubmatch(text); m != nil {
		out, code := sh.runSimple(sh.words(m[2]))
		sh.vars[m[1]] = strings.TrimSuffix(out, "\n")
		return code
	}
	if m := assignPlain.FindStringSubmatch(text); m != nil && !strings.ContainsAny(m[0], " \t") {
		sh.vars[m[1]] = m[2]
		return 0
	}

	words := sh.words(text)
	if len(words) == 0 {
		return 0
	}

	switch words[0] {
	case "exit":
		code := 0
		if len(words) > 1 {
			if n, err := strconv.Atoi(words[1]); err == nil {
				code = n
			}
		}
		sh.exited = true
		return code
	case "trap":
		// trap 'body' EXIT — the body keeps its quotes-off form; expansion
		// happens when the trap runs, matching sh.
		if len(words) == 3 && strings.EqualFold(words[2], "EXIT") {
			sh.trap = words[1]
			return 0
		}
		sh.stderr.WriteString("sandboxfake: unsupported trap\n")
		return 2
	}

	// Pull stdout redirections out of the word list.
	var redirect, redirectFile string
	appendMode := false
	var cmd []string
	for i := 0; i < len(words); i++ {
		if words[i] == ">" || words[i] == ">>" {
			if i+1 >= len(words) {
				sh.stderr.WriteString("sh: syntax error near redirect\n")
				return 2
			}
			redirect = words[i]
			redirectFile = words[i+1]
			appendMode = words[i] == ">>"
			i++
			continue
		}
		cmd = append(cmd, words[i])
	}

	out, code := sh.runSimple(cmd)
	if redirect != "" {
		if err := sh.writeRedirect(redirectFile, out, appendMode); err != nil {
			sh.stderr.WriteString(err.Error() + "\n")
			return 1
		}
	} else {
		sh.stdout.WriteString(out)
	}
	return code
}

// writeRedirect points stdout at a file in the fake tree. The parent must
// exist, as with a real sh: `cat > missing/dir/f` fails.
func (sh *shSession) writeRedirect(p, out string, appendMode bool) error {
	clean, err := sh.sb.resolve(p, sh.cwd)
	if err != nil {
		return err
	}
	e, ok := sh.sb.files[clean]
	if ok && e.isDir {
		return fmt.Errorf("sh: %s: Is a directory", p)
	}
	if !ok {
		parent := path.Dir(clean)
		if pe, ok := sh.sb.files[parent]; !ok || !pe.isDir {
			return fmt.Errorf("sh: %s: No such file or directory", p)
		}
		e = &entry{mode: 0o644, modTime: time.Now()}
		sh.sb.files[clean] = e
	}
	if appendMode {
		e.data = append(e.data, out...)
	} else {
		e.data = append([]byte(nil), out...)
	}
	e.modTime = time.Now()
	return nil
}

// runSimple executes one command and returns its stdout. Diagnostics go to
// the session's stderr.
func (sh *shSession) runSimple(words []string) (string, int) {
	if len(words) == 0 {
		return "", 0
	}
	switch words[0] {
	case "[", "test":
		return sh.cmdTest(words)
	case "stat":
		return sh.cmdStat(words)
	case "mktemp":
		return sh.cmdMktemp(words)
	case "cat":
		return sh.cmdCat(words)
	case "chmod":
		return sh.cmdChmod(words)
	case "mv":
		return sh.cmdMv(words)
	case "rm":
		return sh.cmdRm(words)
	case "mkdir":
		return sh.cmdMkdir(words)
	case "find":
		return sh.cmdFind(words)
	case "echo":
		return strings.Join(words[1:], " ") + "\n", 0
	case "pwd":
		return sh.cwd + "\n", 0
	case "true", ":":
		return "", 0
	case "false":
		return "", 1
	default:
		sh.stderr.WriteString(fmt.Sprintf("sh: %s: command not found\n", words[0]))
		return "", 127
	}
}

// words tokenizes and expands a command line: single quotes are literal,
// double quotes expand $-variables, backslash escapes the next character, and
// $N / $name substitute positional parameters and assignments.
func (sh *shSession) words(line string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
			i++
		case c == '\'':
			i++
			for i < len(line) && line[i] != '\'' {
				cur.WriteByte(line[i])
				i++
			}
			i++
		case c == '"':
			i++
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
					cur.WriteByte(line[i+1])
					i += 2
					continue
				}
				if line[i] == '$' {
					i = sh.expandVar(line, i, &cur)
					continue
				}
				cur.WriteByte(line[i])
				i++
			}
			i++
		case c == '\\':
			if i+1 < len(line) {
				cur.WriteByte(line[i+1])
				i += 2
			} else {
				i++
			}
		case c == '$':
			i = sh.expandVar(line, i, &cur)
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return words
}

// expandVar writes the value of the $-reference at line[i] into cur and
// returns the index just past it.
func (sh *shSession) expandVar(line string, i int, cur *strings.Builder) int {
	j := i + 1
	if j >= len(line) {
		cur.WriteByte('$')
		return j
	}
	if line[j] >= '0' && line[j] <= '9' {
		n := int(line[j] - '0')
		if n < len(sh.params) {
			cur.WriteString(sh.params[n])
		}
		return j + 1
	}
	k := j
	for k < len(line) && (line[k] == '_' || isWordChar(line[k])) {
		k++
	}
	if k > j {
		cur.WriteString(sh.vars[line[j:k]])
		return k
	}
	cur.WriteByte('$')
	return j
}

func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (sh *shSession) errf(format string, args ...any) int {
	sh.stderr.WriteString(fmt.Sprintf(format, args...))
	return 1
}

// operands splits a word list into flag words and operands, honouring the
// `--` terminator.
func operands(words []string) (flags []string, ops []string) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "--" {
			ops = append(ops, words[i+1:]...)
			return flags, ops
		}
		if strings.HasPrefix(w, "-") && w != "-" {
			flags = append(flags, w)
			continue
		}
		ops = append(ops, words[i:]...)
		return flags, ops
	}
	return flags, ops
}

func (sh *shSession) cmdTest(words []string) (string, int) {
	args := words[1:]
	if words[0] == "[" {
		if len(args) == 0 || args[len(args)-1] != "]" {
			return "", sh.errf("sh: [: missing `]'\n")
		}
		args = args[:len(args)-1]
	}
	if len(args) != 2 {
		return "", sh.errf("sh: %s: unsupported expression (only unary -e/-L/-f/-d)\n", words[0])
	}
	op, p := args[0], args[1]
	// -L reports the link itself; the other tests follow symlinks.
	e, err := sh.sb.lookup(sh.ctx, p, op != "-L")
	if err != nil {
		// A failed test is status 1, not an error: `[ -e missing ]` is how
		// the workspace's stat script reaches its exit 42 branch.
		return "", 1
	}
	switch op {
	case "-e":
		return "", 0
	case "-L":
		if e.isLink {
			return "", 0
		}
	case "-f":
		if !e.isDir && !e.isLink {
			return "", 0
		}
	case "-d":
		if e.isDir {
			return "", 0
		}
	default:
		return "", sh.errf("sh: %s: unsupported unary %s\n", words[0], op)
	}
	return "", 1
}

func (sh *shSession) cmdStat(words []string) (string, int) {
	follow := false
	format := ""
	var ops []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-L":
			follow = true
		case w == "-c" && i+1 < len(words):
			i++
			format = words[i]
		case strings.HasPrefix(w, "-c") && len(w) > 2:
			format = w[2:]
		case w == "--":
			ops = append(ops, words[i+1:]...)
			i = len(words)
		default:
			ops = append(ops, w)
		}
	}
	if len(ops) != 1 || format == "" {
		return "", sh.errf("stat: missing operand\n")
	}
	p := ops[0]
	e, err := sh.sb.lookup(sh.ctx, p, follow)
	if err != nil {
		return "", sh.errf("stat: cannot statx '%s': No such file or directory\n", p)
	}
	out, code := sh.statFormat(format, e, p)
	if code != 0 {
		return "", code
	}
	return out + "\n", 0
}

func (sh *shSession) statFormat(format string, e *entry, p string) (string, int) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(format) {
			b.WriteByte('%')
			break
		}
		switch format[i] {
		case 'F':
			b.WriteString(e.kindString())
		case 's':
			b.WriteString(strconv.FormatInt(e.size(), 10))
		case 'a':
			b.WriteString(modeOctal(e.mode))
		case 'Y':
			b.WriteString(strconv.FormatInt(e.modTime.Unix(), 10))
		case 'y':
			b.WriteString(e.kindChar())
		case 'f':
			b.WriteString(path.Base(p))
		case 'n':
			b.WriteString(p)
		case '%':
			b.WriteByte('%')
		default:
			return "", sh.errf("sandboxfake: stat: unsupported specifier %%%c\n", format[i])
		}
	}
	return b.String(), 0
}

func (sh *shSession) cmdMktemp(words []string) (string, int) {
	_, ops := operands(words[1:])
	if len(ops) != 1 {
		return "", sh.errf("mktemp: missing operand\n")
	}
	tpl := ops[0]
	// Replace the trailing run of X's with a counter-derived suffix, the way
	// mktemp replaces them with random characters.
	i := len(tpl)
	for i > 0 && tpl[i-1] == 'X' {
		i--
	}
	if len(tpl)-i < 3 {
		return "", sh.errf("mktemp: too few X's in template %s\n", tpl)
	}
	sh.sb.tmp++
	name := tpl[:i] + fmt.Sprintf("%06d", sh.sb.tmp)
	clean, err := sh.sb.resolve(name, sh.cwd)
	if err != nil {
		return "", sh.errf("mktemp: %v\n", err)
	}
	if parent := path.Dir(clean); !sh.sb.dirExists(parent) {
		return "", sh.errf("mktemp: failed to create file '%s': No such file or directory\n", name)
	}
	sh.sb.files[clean] = &entry{mode: 0o600, modTime: time.Now()}
	return name + "\n", 0
}

func (sh *shSession) cmdCat(words []string) (string, int) {
	if len(words) == 1 {
		// cat with no operands copies stdin; the workspace uses it with a
		// stdout redirect to receive the file content.
		return string(sh.stdin), 0
	}
	var b strings.Builder
	for _, p := range words[1:] {
		e, err := sh.sb.lookup(sh.ctx, p, true)
		if err != nil {
			return "", sh.errf("cat: %s: No such file or directory\n", p)
		}
		if e.isDir {
			return "", sh.errf("cat: %s: Is a directory\n", p)
		}
		b.Write(e.data)
	}
	return b.String(), 0
}

func (sh *shSession) cmdChmod(words []string) (string, int) {
	_, ops := operands(words[1:])
	if len(ops) < 2 {
		return "", sh.errf("chmod: missing operand\n")
	}
	perm, err := strconv.ParseUint(ops[0], 8, 32)
	if err != nil {
		return "", sh.errf("chmod: invalid mode: %s\n", ops[0])
	}
	for _, p := range ops[1:] {
		e, err := sh.sb.lookup(sh.ctx, p, true)
		if err != nil {
			return "", sh.errf("chmod: cannot access '%s': No such file or directory\n", p)
		}
		e.mode = toMode(fs.FileMode(perm))
		e.modTime = time.Now()
	}
	return "", 0
}

func (sh *shSession) cmdMv(words []string) (string, int) {
	flags, ops := operands(words[1:])
	for _, f := range flags {
		if f != "-f" {
			return "", sh.errf("sandboxfake: mv: unsupported flag %s\n", f)
		}
	}
	if len(ops) != 2 {
		return "", sh.errf("mv: missing operand\n")
	}
	src, dst := ops[0], ops[1]
	srcClean, err := sh.sb.resolve(src, sh.cwd)
	if err != nil {
		return "", sh.errf("mv: %v\n", err)
	}
	e, ok := sh.sb.files[srcClean]
	if !ok {
		return "", sh.errf("mv: cannot stat '%s': No such file or directory\n", src)
	}
	if e.isDir {
		// The workspace only moves mktemp files into place; directory moves
		// would need subtree rekeying that nothing exercises.
		return "", sh.errf("sandboxfake: mv: moving a directory is not supported\n")
	}
	dstClean, err := sh.sb.resolve(dst, sh.cwd)
	if err != nil {
		return "", sh.errf("mv: %v\n", err)
	}
	if existing, ok := sh.sb.files[dstClean]; ok && existing.isDir {
		// A real mv would move into the directory; the workspace's rename
		// usage would silently misplace the file, so refuse instead.
		return "", sh.errf("sandboxfake: mv: destination is a directory\n")
	}
	if parent := path.Dir(dstClean); !sh.sb.dirExists(parent) {
		return "", sh.errf("mv: cannot move '%s' to '%s': No such file or directory\n", src, dst)
	}
	delete(sh.sb.files, srcClean)
	sh.sb.files[dstClean] = e
	e.modTime = time.Now()
	return "", 0
}

func (sh *shSession) cmdRm(words []string) (string, int) {
	flags, ops := operands(words[1:])
	force, recursive := false, false
	for _, f := range flags {
		switch f {
		case "-f":
			force = true
		case "-r", "-R", "-rf", "-fr":
			recursive = true
			if f == "-rf" || f == "-fr" {
				force = true
			}
		default:
			return "", sh.errf("sandboxfake: rm: unsupported flag %s\n", f)
		}
	}
	for _, p := range ops {
		clean, err := sh.sb.resolve(p, sh.cwd)
		if err != nil {
			return "", sh.errf("rm: %v\n", err)
		}
		e, ok := sh.sb.files[clean]
		if !ok {
			if !force {
				return "", sh.errf("rm: cannot remove '%s': No such file or directory\n", p)
			}
			continue
		}
		if e.isDir && !recursive {
			return "", sh.errf("rm: cannot remove '%s': Is a directory\n", p)
		}
		sh.sb.removeLocked(clean)
	}
	return "", 0
}

func (sh *shSession) cmdMkdir(words []string) (string, int) {
	flags, ops := operands(words[1:])
	parents := false
	for _, f := range flags {
		if f == "-p" {
			parents = true
			continue
		}
		return "", sh.errf("sandboxfake: mkdir: unsupported flag %s\n", f)
	}
	if len(ops) != 1 {
		return "", sh.errf("mkdir: missing operand\n")
	}
	clean, err := sh.sb.resolve(ops[0], sh.cwd)
	if err != nil {
		return "", sh.errf("mkdir: %v\n", err)
	}
	if e, ok := sh.sb.files[clean]; ok {
		if e.isDir && parents {
			return "", 0
		}
		return "", sh.errf("mkdir: cannot create directory '%s': File exists\n", ops[0])
	}
	if !parents {
		if parent := path.Dir(clean); !sh.sb.dirExists(parent) {
			return "", sh.errf("mkdir: cannot create directory '%s': No such file or directory\n", ops[0])
		}
	}
	if err := sh.sb.mkdirParentsLocked(clean, 0o755); err != nil {
		return "", sh.errf("mkdir: cannot create directory '%s': %v\n", ops[0], err)
	}
	return "", 0
}

func (sh *shSession) cmdFind(words []string) (string, int) {
	// find PATH -mindepth N -maxdepth N -printf FORMAT — the exact form
	// sandboxWorkspace's ReadDir emits, and nothing else.
	if len(words) < 2 {
		return "", sh.errf("find: missing starting point\n")
	}
	root, err := sh.sb.resolve(words[1], sh.cwd)
	if err != nil {
		return "", sh.errf("find: %v\n", err)
	}
	mindepth, maxdepth := 0, -1
	format := "%p\n"
	for i := 2; i < len(words); i++ {
		switch {
		case words[i] == "-mindepth" && i+1 < len(words):
			if n, err := strconv.Atoi(words[i+1]); err == nil {
				mindepth = n
			}
			i++
		case words[i] == "-maxdepth" && i+1 < len(words):
			if n, err := strconv.Atoi(words[i+1]); err == nil {
				maxdepth = n
			}
			i++
		case words[i] == "-printf" && i+1 < len(words):
			format = words[i+1]
			i++
		default:
			return "", sh.errf("sandboxfake: find: unsupported predicate %s\n", words[i])
		}
	}
	e, ok := sh.sb.files[root]
	if !ok {
		return "", sh.errf("find: '%s': No such file or directory\n", words[1])
	}
	// find does not follow symlinks, so a symlink root is offered as itself.
	if !e.isDir {
		if mindepth > 0 {
			return "", 0
		}
		return sh.printfFormat(format, e, root)
	}
	if maxdepth >= 0 && maxdepth < 1 {
		// Only the root itself can be listed, and -mindepth 1 excludes it.
		return "", 0
	}
	var b strings.Builder
	for _, name := range sh.sb.children(root) {
		child := path.Join(root, name)
		ce := sh.sb.files[child]
		out, code := sh.printfFormat(format, ce, child)
		if code != 0 {
			return "", code
		}
		b.WriteString(out)
	}
	return b.String(), 0
}

// printfFormat expands find -printf escapes and specifiers. Only the
// specifiers the workspace's ReadDir uses (%y, %f) plus a few trivial ones are
// supported; anything else is an error rather than empty output, so a format
// change is caught by tests.
func (sh *shSession) printfFormat(format string, e *entry, p string) (string, int) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch {
		case c == '\\' && i+1 < len(format):
			i++
			switch format[i] {
			case '0':
				b.WriteByte(0)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(format[i])
			}
		case c == '%':
			i++
			if i >= len(format) {
				b.WriteByte('%')
				break
			}
			switch format[i] {
			case 'y':
				b.WriteString(e.kindChar())
			case 'f':
				b.WriteString(path.Base(p))
			case 'p':
				b.WriteString(p)
			case 's':
				b.WriteString(strconv.FormatInt(e.size(), 10))
			case 'a':
				b.WriteString(modeOctal(e.mode))
			case '%':
				b.WriteByte('%')
			default:
				return "", sh.errf("sandboxfake: find: unsupported specifier %%%c\n", format[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), 0
}

// dirExists reports whether p is a directory entry. Callers hold s.mu.
func (s *Sandbox) dirExists(p string) bool {
	e, ok := s.files[p]
	return ok && e.isDir
}
