package cli

import (
	"fmt"
	"os"

	"github.com/stvnksslr/gogloader/internal/ui"
)

// warnf prints a highlighted notice to stderr.
func warnf(format string, args ...any) {
	st := ui.For(os.Stderr)
	fmt.Fprintln(os.Stderr, st.Yellow("!")+" "+fmt.Sprintf(format, args...))
}
