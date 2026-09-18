package nxartifact

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/nimbit-platform/nx-cache/internal/store"
)

const (
	maxTerminalBytes = 64 * 1024
	maxInspectBytes  = 8 << 20 // decompressed gzip/tar scanned for metadata
	maxTarEntries    = 4000
	maxInspectPaths  = 256
)

var (
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	nxRunRe       = regexp.MustCompile(`(?i)(?:^|\n)\s*>\s*nx(?:\.exe)?\s+run\s+(\S+)`)
	nxCommandRe   = regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)*(?:(?:pnpm|npm|npx|bunx|yarn)\s+(?:(?:exec|run)\s+)?)?nx(?:\.exe)?\s+(.+)$`)
	nxTargetRe    = regexp.MustCompile(`(?i)(?:running|ran|successfully ran)\s+target\s+(\S+)\s+for project\s+(\S+)`)
	toolProjectRe = regexp.MustCompile(`(?im)^\s*running\s+([A-Za-z0-9._-]+)\s+for\s+project:\s+([A-Za-z0-9_@./-]+)`)
	commandRe     = regexp.MustCompile(`(?m)^\s*>\s+(.+?)\s*$`)
)

// Inspect reads an Nx remote-cache payload (gzip tar, as produced by HttpRemoteCache)
// and extracts project/target from terminalOutput plus output paths.
func Inspect(r io.Reader) store.TaskInfo {
	info, _ := inspect(r)
	return info
}

func inspect(r io.Reader) (store.TaskInfo, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(2)
	var src io.Reader = br
	if len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			_, _ = io.Copy(io.Discard, br)
			return store.TaskInfo{}, err
		}
		defer gz.Close()
		src = gz
	}

	src = io.LimitReader(src, maxInspectBytes)
	tr := tar.NewReader(src)
	var info store.TaskInfo
	var terminal string
	var paths []string
	foundTerminal := false
	for i := 0; i < maxTarEntries; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return finalize(info, terminal, paths), err
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(hdr.Name, "\\", "/")), "/")
		base := path.Base(name)
		switch {
		case base == "terminalOutput" || name == "terminalOutput":
			var buf bytes.Buffer
			_, _ = io.CopyN(&buf, tr, maxTerminalBytes)
			_, _ = io.Copy(io.Discard, io.LimitReader(tr, maxTerminalBytes))
			terminal = buf.String()
			foundTerminal = true
		case base == "code" || name == "code" || base == "source":
			_, _ = io.Copy(io.Discard, io.LimitReader(tr, 64))
		default:
			if (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeDir) && name != "" && name != "." && len(paths) < maxInspectPaths {
				paths = append(paths, name)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(tr, 1<<20))
		}
		if foundTerminal {
			break
		}
	}
	return finalize(info, terminal, paths), nil
}

func FromHeaders(h http.Header) store.TaskInfo {
	info := store.TaskInfo{
		Project: firstHeader(h, "X-Nx-Project", "Nx-Project"),
		Target:  firstHeader(h, "X-Nx-Target", "Nx-Target"),
		Config:  firstHeader(h, "X-Nx-Configuration", "Nx-Configuration"),
	}
	if info.Kind == "" {
		info.Kind = KindFromTarget(info.Target)
	}
	return info
}

func Merge(primary, fallback store.TaskInfo) store.TaskInfo {
	out := fallback
	if primary.Project != "" {
		out.Project = primary.Project
	}
	if primary.Target != "" {
		out.Target = primary.Target
	}
	if primary.Config != "" {
		out.Config = primary.Config
	}
	if primary.Kind != "" {
		out.Kind = primary.Kind
	} else if fallback.Kind != "" {
		out.Kind = fallback.Kind
	} else {
		out.Kind = KindFromTarget(out.Target)
	}
	return out
}

func KindFromTarget(target string) string {
	t := strings.ToLower(strings.TrimSpace(target))
	if t == "" {
		return ""
	}
	switch t {
	case "e2e", "e2e-ci", "cypress", "playwright", "playwright-e2e":
		return "e2e"
	case "lint", "eslint", "prettier", "stylelint":
		return "lint"
	case "test", "test-ci", "jest", "vitest", "unit-test", "spec":
		return "test"
	case "typecheck", "check-types", "tsc":
		return "typecheck"
	case "build", "compile", "bundle", "package", "prerender":
		return "build"
	}
	switch {
	case strings.Contains(t, "e2e") || strings.Contains(t, "playwright") || strings.Contains(t, "cypress"):
		return "e2e"
	case strings.Contains(t, "lint"):
		return "lint"
	case strings.Contains(t, "test") || strings.Contains(t, "jest") || strings.Contains(t, "vitest"):
		return "test"
	case strings.Contains(t, "typecheck"):
		return "typecheck"
	case strings.Contains(t, "build") || strings.Contains(t, "compile"):
		return "build"
	default:
		return t
	}
}

func KindFromPaths(paths []string) string {
	for _, p := range paths {
		l := strings.ToLower(p)
		switch {
		case strings.Contains(l, "coverage") || strings.Contains(l, "jest") || strings.Contains(l, "vitest"):
			return "test"
		case strings.Contains(l, "eslint") || strings.Contains(l, ".eslintcache"):
			return "lint"
		case strings.Contains(l, "playwright") || strings.Contains(l, "cypress"):
			return "e2e"
		case strings.Contains(l, "/dist/") || strings.HasPrefix(l, "dist/") ||
			strings.Contains(l, "/.next/") || strings.Contains(l, "/build/") ||
			strings.Contains(l, "outputs/dist"):
			return "build"
		}
	}
	return ""
}

// Pack builds a gzip tar in the layout Nx HttpRemoteCache uploads:
// output files, then terminalOutput, then a 4-byte big-endian exit code.
func Pack(terminal string, code uint32, files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	term := []byte(terminal)
	if err := tw.WriteHeader(&tar.Header{Name: "terminalOutput", Mode: 0o644, Size: int64(len(term))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(term); err != nil {
		return nil, err
	}
	var codeBuf [4]byte
	binary.BigEndian.PutUint32(codeBuf[:], code)
	if err := tw.WriteHeader(&tar.Header{Name: "code", Mode: 0o644, Size: 4}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(codeBuf[:]); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func finalize(info store.TaskInfo, terminal string, paths []string) store.TaskInfo {
	text := stripANSI(terminal)
	if p, t, c, ok := parseNxRun(text); ok {
		info.Project, info.Target, info.Config = p, t, c
	} else if p, t, c, ok := parseNxCommand(text); ok {
		info.Project, info.Target, info.Config = p, t, c
	} else if p, t, ok := parseToolProject(text); ok {
		info.Project, info.Target = p, t
	} else if p, t, ok := parseNxTargetLine(text); ok {
		if info.Project == "" {
			info.Project = p
		}
		if info.Target == "" {
			info.Target = t
		}
	} else if command, ok := parseCommand(text); ok {
		if info.Target == "" {
			info.Target = command
		}
		if info.Kind == "" {
			info.Kind = commandKind(command)
		}
	}
	if info.Project == "" {
		info.Project = projectFromPaths(paths)
	}
	if info.Kind == "" {
		info.Kind = KindFromTarget(info.Target)
	}
	pathKind := KindFromPaths(paths)
	if info.Kind == "" {
		info.Kind = pathKind
	}
	if info.Target == "" {
		info.Target = pathKind
	}
	return info
}

func parseCommand(text string) (string, bool) {
	matches := commandRe.FindAllStringSubmatch(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		fields := strings.Fields(matches[i][1])
		if len(fields) == 0 {
			continue
		}
		for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "pnpm":
			fields = fields[1:]
			if len(fields) > 0 && (fields[0] == "exec" || fields[0] == "run") {
				fields = fields[1:]
			}
		case "npm":
			fields = fields[1:]
			if len(fields) > 0 && (fields[0] == "exec" || fields[0] == "run") {
				fields = fields[1:]
			}
		case "npx", "bunx", "yarn":
			fields = fields[1:]
		default:
			continue
		}
		if len(fields) == 0 {
			continue
		}
		end := len(fields)
		for j, field := range fields {
			if strings.HasPrefix(field, "-") {
				end = j
				break
			}
		}
		if end == 0 {
			continue
		}
		return strings.Join(fields[:end], " "), true
	}
	return "", false
}

func projectFromPaths(paths []string) string {
	for _, name := range paths {
		parts := strings.Split(name, "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "apps" || parts[i] == "libs" {
				return parts[i+1]
			}
		}
	}
	return ""
}

func commandKind(command string) string {
	kind := KindFromTarget(command)
	switch kind {
	case "build", "test", "lint", "e2e", "typecheck", "serve":
		return kind
	default:
		return ""
	}
}

func parseNxRun(text string) (project, target, config string, ok bool) {
	m := nxRunRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", "", "", false
	}
	return parseTaskToken(cleanToken(m[1]))
}

func parseNxCommand(text string) (project, target, config string, ok bool) {
	m := nxCommandRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", "", "", false
	}
	fields := strings.Fields(m[1])
	if len(fields) < 2 || strings.HasPrefix(fields[0], "-") {
		return "", "", "", false
	}
	switch strings.ToLower(fields[0]) {
	case "running", "ran", "successfully":
		return "", "", "", false
	}
	if fields[0] == "run" {
		return parseTaskToken(cleanToken(fields[1]))
	}
	if fields[0] == "run-many" {
		target, ok := parseSingleNxTarget(fields[1:])
		if !ok {
			return "", "", "", false
		}
		return "", target, "", true
	}
	if strings.HasPrefix(fields[1], "-") {
		return "", "", "", false
	}
	return cleanToken(fields[1]), cleanToken(fields[0]), "", true
}

func parseSingleNxTarget(args []string) (string, bool) {
	var targets []string
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		if name != "-t" && name != "--target" && name != "--targets" {
			continue
		}
		if hasValue {
			targets = append(targets, strings.Split(value, ",")...)
			continue
		}
		for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			targets = append(targets, strings.Split(args[i+1], ",")...)
			i++
		}
	}
	if len(targets) != 1 || targets[0] == "" {
		return "", false
	}
	return cleanToken(targets[0]), true
}

func cleanToken(token string) string {
	return strings.TrimRightFunc(token, func(r rune) bool {
		return unicode.IsPunct(r) && r != '/' && r != '@' && r != '-' && r != '_' && r != '.'
	})
}

func parseTaskToken(token string) (project, target, config string, ok bool) {
	parts := strings.Split(token, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	project, target = parts[0], parts[1]
	if len(parts) > 2 {
		config = strings.Join(parts[2:], ":")
	}
	return project, target, config, true
}

func parseNxTargetLine(text string) (project, target string, ok bool) {
	m := nxTargetRe.FindStringSubmatch(text)
	if len(m) < 3 {
		return "", "", false
	}
	return strings.TrimRight(m[2], ".:"), m[1], true
}

func parseToolProject(text string) (project, target string, ok bool) {
	m := toolProjectRe.FindStringSubmatch(text)
	if len(m) < 3 {
		return "", "", false
	}
	kind := commandKind(m[1])
	if kind == "" {
		return "", "", false
	}
	return cleanToken(m[2]), kind, true
}

func stripANSI(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return ansiRe.ReplaceAllString(s, "")
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			return v
		}
	}
	return ""
}
