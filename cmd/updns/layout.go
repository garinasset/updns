package main

import (
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

type layoutWriter struct {
	io.Writer
	width int
}

var terminalEscape = regexp.MustCompile("\\x1b\\[[0-?]*[ -/]*[@-~]|\\x1b\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)")

func cleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, terminalEscape.ReplaceAllString(text, ""))
}

func (w *layoutWriter) Write(data []byte) (int, error) {
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		newline := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\n")
		if err := w.writeLine(line, newline); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (w *layoutWriter) writeLine(line string, newline bool) error {
	prefix := ""
	text := line
	if strings.HasPrefix(line, "│") {
		text = strings.TrimPrefix(line, "│")
		spaces := len(text) - len(strings.TrimLeft(text, " "))
		prefix = "│" + strings.Repeat(" ", min(spaces, max(0, w.width-3)))
		if w.width < 3 {
			prefix = ""
		}
		text = strings.TrimLeft(text, " ")
	}
	continuation := prefix
	if index := strings.Index(text, " : "); index >= 0 {
		label, value := text[:index], text[index+3:]
		fieldPrefix := prefix + label + " : "
		if uniseg.StringWidth(fieldPrefix) <= w.width-4 {
			prefix = fieldPrefix
			continuation = "│" + strings.Repeat(" ", uniseg.StringWidth(fieldPrefix)-1)
			text = value
		} else {
			if err := w.writeLine(prefix+strings.TrimSpace(label)+":", true); err != nil {
				return err
			}
			prefix = "│" + strings.Repeat(" ", min(4, max(0, w.width-3)))
			if w.width < 3 {
				prefix = ""
			}
			continuation = prefix
			text = value
		}
	}
	graphemes := uniseg.NewGraphemes(text)
	var chunk strings.Builder
	columns := uniseg.StringWidth(prefix)
	chunk.WriteString(prefix)
	for graphemes.Next() {
		cluster, width := graphemes.Str(), graphemes.Width()
		if width > w.width-uniseg.StringWidth(continuation) {
			// A grapheme wider than the entire available line cannot be split.
			cluster, width = "?", 1
		}
		if columns+width > w.width {
			if _, err := io.WriteString(w.Writer, chunk.String()+"\n"); err != nil {
				return err
			}
			chunk.Reset()
			chunk.WriteString(continuation)
			columns = uniseg.StringWidth(continuation)
		}
		chunk.WriteString(cluster)
		columns += width
	}
	if newline {
		chunk.WriteByte('\n')
	}
	_, err := io.WriteString(w.Writer, chunk.String())
	return err
}
