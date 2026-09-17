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

const maxTerminalBytes = 64 * 1024

var (
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	nxRunRe      = regexp.MustCompile(`(?i)(?:^|\n)\s*>\s*nx(?:\.exe)?\s+run\s+(\S+)`)
	nxTargetRe   = regexp.MustCompile(`(?i)(?:running|ran|successfully ran)\s+target\s+(\S+)\s+for project\s+(\S+)`)
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

	tr := tar.NewReader(src)
	var info store.TaskInfo
	var terminal string
	var paths []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_, _ = io.Copy(io.Discard, src)
			return finalize(info, terminal, paths), err
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(hdr.Name, "\\", "/")), "/")
		base := path.Base(name)
		switch {
		case base == "terminalOutput" || name == "terminalOutput":
			var buf bytes.Buffer
			_, _ = io.CopyN(&buf, tr, maxTerminalBytes)
			_, _ = io.Copy(io.Discard, tr)
			terminal = buf.String()
		case base == "code" || name == "code" || base == "source":
			_, _ = io.Copy(io.Discard, tr)
		default:
			if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeDir {
				if name != "" && name != "." {
					paths = append(paths, name)
				}
			}
			_, _ = io.Copy(io.Discard, tr)
		}
	}
	_, _ = io.Copy(io.Discard, src)
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
	} else {
		out.Kind = KindFromTarget(out.Target)
		if out.Kind == "" {
			out.Kind = fallback.Kind
		}
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
	joined := strings.ToLower(strings.Join(paths, "\n"))
	switch {
	case strings.Contains(joined, "coverage") || strings.Contains(joined, "jest") || strings.Contains(joined, "vitest"):
		return "test"
	case strings.Contains(joined, "eslint") || strings.Contains(joined, ".eslintcache"):
		return "lint"
	case strings.Contains(joined, "playwright") || strings.Contains(joined, "cypress"):
		return "e2e"
	case strings.Contains(joined, "/dist/") || strings.HasPrefix(joined, "dist/") ||
		strings.Contains(joined, "/.next/") || strings.Contains(joined, "/build/") ||
		strings.Contains(joined, "outputs/dist"):
		return "build"
	default:
		return ""
	}
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
	} else if p, t, ok := parseNxTargetLine(text); ok {
		if info.Project == "" {
			info.Project = p
		}
		if info.Target == "" {
			info.Target = t
		}
	}
	if info.Kind == "" {
		info.Kind = KindFromTarget(info.Target)
	}
	if info.Kind == "" {
		info.Kind = KindFromPaths(paths)
	}
	return info
}

func parseNxRun(text string) (project, target, config string, ok bool) {
	m := nxRunRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", "", "", false
	}
	token := strings.TrimRightFunc(m[1], func(r rune) bool {
		return unicode.IsPunct(r) && r != '/' && r != '@' && r != '-' && r != '_' && r != '.'
	})
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
