package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/gog"
	"github.com/stvnksslr/gogloader/internal/ui"
)

func newListCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:     "list [filter]",
		Aliases: []string{"ls"},
		Short:   "List games in your library",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := gog.NewClient()
			if err != nil {
				return err
			}
			lib, err := c.CachedLibrary(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			st := ui.For(out)
			filter := strings.ToLower(strings.Join(args, " "))
			mark := func(ok bool) string {
				if ok {
					return st.Green("✓")
				}
				return st.Dim("·")
			}
			lib = slices.Clone(lib)
			slices.SortStableFunc(lib, func(a, b gog.LibraryProduct) int {
				return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
			})
			t := &ui.Table{
				Header:   []string{"TITLE", "SLUG", "WIN", "MAC", "LINUX"},
				MaxWidth: []int{40},
			}
			for _, p := range lib {
				if filter != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Slug), filter) {
					continue
				}
				t.Row(p.Title, st.Cyan(p.Slug), mark(p.WorksOn.Windows), mark(p.WorksOn.Mac), mark(p.WorksOn.Linux))
			}
			if t.Len() == 0 {
				fmt.Fprintf(out, "No games in your library match %q.\n", filter)
				return nil
			}
			t.Render(out, st)
			if st.Enabled() {
				fmt.Fprintln(out, st.Dim("\n"+ui.Plural(t.Len(), "game")))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&refresh, "refresh", "r", false,
		fmt.Sprintf("ignore the library cache (entries are reused for %s)", gog.LibraryTTL))
	return cmd
}
