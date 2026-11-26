package source

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxAnnotationFileBytes = 64 * 1024

// ParseAnnotationTitle extracts the top-level `title: "..."` or `title: '...'` field
// from an Antigravity annotations/<conv_id>.pbtxt file, decoding protobuf text-format
// escape sequences (including octal/hex UTF-8 byte escapes).
func ParseAnnotationTitle(raw string) string {
	if raw == "" {
		return ""
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "title") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "title")
		// Ensure key boundary (must be followed by whitespace or ':').
		if rest == "" || (rest[0] != ':' && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		val := strings.TrimSpace(rest[1:])
		if len(val) < 2 {
			continue
		}
		quote := val[0]
		if quote != '"' && quote != '\'' {
			continue
		}
		// Find matching unescaped closing quote.
		end := -1
		for i := 1; i < len(val); i++ {
			if val[i] == '\\' {
				i++
				continue
			}
			if val[i] == quote {
				end = i
				break
			}
		}
		if end <= 1 {
			continue
		}
		if title := strings.TrimSpace(unescapePbtxtString(val[1:end])); title != "" {
			return title
		}
	}
	return ""
}

// ReadAnnotationTitle reads <rootDir>/annotations/<convID>.pbtxt (trying exact and
// lowercase conversation IDs) and returns the parsed title, or "" if absent/unreadable.
func ReadAnnotationTitle(rootDir, convID string) string {
	rootDir = strings.TrimSpace(rootDir)
	convID = strings.TrimSpace(convID)
	if rootDir == "" || convID == "" {
		return ""
	}
	annDir := filepath.Join(rootDir, "annotations")
	candidates := []string{convID}
	if lower := strings.ToLower(convID); lower != convID {
		candidates = append(candidates, lower)
	}
	for _, id := range candidates {
		p := filepath.Join(annDir, id+".pbtxt")
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Size() <= 0 || fi.Size() > maxAnnotationFileBytes {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if title := ParseAnnotationTitle(string(raw)); title != "" {
			return title
		}
	}
	return ""
}

// unescapePbtxtString decodes protobuf text-format string escapes (\n, \t, \", \', \\,
// \ooo octal bytes, \xhh hex bytes, \uhhhh / \Uhhhhhhhh unicode escapes).
func unescapePbtxtString(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var out bytes.Buffer
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case '"':
			out.WriteByte('"')
		case '\'':
			out.WriteByte('\'')
		case '\\':
			out.WriteByte('\\')
		case 'a':
			out.WriteByte('\a')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'v':
			out.WriteByte('\v')
		case 'x', 'X':
			// 1..2 hex digits.
			start := i + 1
			end := start
			for end < len(s) && end-start < 2 && isHexDigit(s[end]) {
				end++
			}
			if end > start {
				if v, err := strconv.ParseUint(s[start:end], 16, 8); err == nil {
					out.WriteByte(byte(v))
					i = end - 1
					continue
				}
			}
			out.WriteByte('\\')
			out.WriteByte(s[i])
		case 'u':
			if i+4 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					var buf [utf8.UTFMax]byte
					n := utf8.EncodeRune(buf[:], rune(v))
					out.Write(buf[:n])
					i += 4
					continue
				}
			}
			out.WriteByte('\\')
			out.WriteByte('u')
		case 'U':
			if i+8 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+9], 16, 32); err == nil && v <= utf8.MaxRune {
					var buf [utf8.UTFMax]byte
					n := utf8.EncodeRune(buf[:], rune(v))
					out.Write(buf[:n])
					i += 8
					continue
				}
			}
			out.WriteByte('\\')
			out.WriteByte('U')
		default:
			if s[i] >= '0' && s[i] <= '7' {
				// 1..3 octal digits representing a raw UTF-8 byte.
				start := i
				end := i + 1
				for end < len(s) && end-start < 3 && s[end] >= '0' && s[end] <= '7' {
					end++
				}
				if v, err := strconv.ParseUint(s[start:end], 8, 16); err == nil && v <= 0xff {
					out.WriteByte(byte(v))
					i = end - 1
					continue
				}
			}
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
