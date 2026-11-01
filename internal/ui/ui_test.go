package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestTableAlignsStyledCells(t *testing.T) {
	st := Styler{on: true}
	tb := &Table{Header: []string{"A", "B"}, Align: []Align{Left, Right}, MaxWidth: []int{6}}
	tb.Row("long title here", st.Green("1"))
	tb.Row("x", st.Green("100"))
	var buf bytes.Buffer
	tb.Render(&buf, st)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	w := Width(lines[0])
	for _, l := range lines {
		if Width(l) != w {
			t.Fatalf("misaligned rows:\n%s", buf.String())
		}
	}
	if !strings.Contains(lines[1], "long…") {
		t.Fatalf("expected truncation, got %q", lines[1])
	}
	// Disabled styler (piped output): no truncation.
	buf.Reset()
	tb.Render(&buf, Styler{})
	if !strings.Contains(buf.String(), "long title here") {
		t.Fatalf("plain output wrong:\n%s", buf.String())
	}
}
