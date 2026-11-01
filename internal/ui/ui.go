// Package ui holds small terminal formatting helpers: styling that switches
// off when output isn't a terminal (or NO_COLOR is set) and an aligned table
// renderer that measures visible width, so styled cells still line up.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Styler applies ANSI styles when enabled.
type Styler struct{ on bool }

// For returns a Styler that is enabled only if w is a terminal and NO_COLOR
// is unset. CLICOLOR_FORCE=1 forces it on (e.g. for `| less -R`).
func For(w io.Writer) Styler {
	if _, ok := os.LookupEnv("NO_COLOR"); ok || os.Getenv("TERM") == "dumb" {
		return Styler{}
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return Styler{on: true}
	}
	f, ok := w.(*os.File)
	if !ok {
		return Styler{}
	}
	st, err := f.Stat()
	return Styler{on: err == nil && st.Mode()&os.ModeCharDevice != 0}
}

// Enabled reports whether styling (and therefore a TTY) is active.
func (s Styler) Enabled() bool { return s.on }

func (s Styler) wrap(code, t string) string {
	if !s.on || t == "" {
		return t
	}
	return "\033[" + code + "m" + t + "\033[0m"
}

func (s Styler) Bold(t string) string   { return s.wrap("1", t) }
func (s Styler) Dim(t string) string    { return s.wrap("2", t) }
func (s Styler) Green(t string) string  { return s.wrap("32", t) }
func (s Styler) Yellow(t string) string { return s.wrap("33", t) }
func (s Styler) Red(t string) string    { return s.wrap("31", t) }
func (s Styler) Cyan(t string) string   { return s.wrap("36", t) }

// Width is the visible width of s (runes, ignoring ANSI escapes).
func Width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j + 1
				continue
			}
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		n++
	}
	return n
}

// Align controls column alignment.
type Align int

const (
	Left Align = iota
	Right
)

// Table renders aligned columns. Cells may already contain styling.
type Table struct {
	Header []string
	Align  []Align // per column; missing entries default to Left
	Indent string
	Gap    int // spaces between columns (default 2)
	// MaxWidth caps a column's width (0 = unlimited); longer plain-text
	// cells are truncated with "…". Applied only when the Styler is enabled
	// (a terminal), so piped output stays complete.
	MaxWidth []int
	rows     [][]string
}

// Row appends a row.
func (t *Table) Row(cells ...string) { t.rows = append(t.rows, cells) }

// Len is the number of rows added.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table; the header is styled dim+bold via st.
func (t *Table) Render(w io.Writer, st Styler) {
	gap := t.Gap
	if gap == 0 {
		gap = 2
	}
	all := t.rows
	if st.Enabled() && len(t.MaxWidth) > 0 {
		all = make([][]string, len(t.rows))
		for i, r := range t.rows {
			all[i] = make([]string, len(r))
			for j, c := range r {
				if j < len(t.MaxWidth) && t.MaxWidth[j] > 0 {
					c = Truncate(c, t.MaxWidth[j])
				}
				all[i][j] = c
			}
		}
	}
	rows := all
	if len(t.Header) > 0 {
		all = append([][]string{t.Header}, rows...)
	}
	var widths []int
	for _, r := range all {
		for i, c := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], Width(c))
		}
	}
	line := func(r []string, style func(string) string) {
		var b strings.Builder
		b.WriteString(t.Indent)
		for i, c := range r {
			pad := widths[i] - Width(c)
			last := i == len(r)-1
			right := i < len(t.Align) && t.Align[i] == Right
			if right {
				b.WriteString(strings.Repeat(" ", pad))
			}
			b.WriteString(style(c))
			if !last {
				if !right {
					b.WriteString(strings.Repeat(" ", pad))
				}
				b.WriteString(strings.Repeat(" ", gap))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	if len(t.Header) > 0 {
		line(t.Header, func(s string) string { return st.Bold(st.Dim(s)) })
	}
	for _, r := range rows {
		line(r, func(s string) string { return s })
	}
}

// Truncate shortens plain (unstyled) s to at most n visible runes, ending
// in "…". Styled strings are returned unchanged.
func Truncate(s string, n int) string {
	if n <= 0 || Width(s) <= n || strings.IndexByte(s, 0x1b) >= 0 {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// Plural returns "1 part" / "3 parts".
func Plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
