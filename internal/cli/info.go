package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/download"
	"github.com/stvnksslr/gogloader/internal/gog"
	"github.com/stvnksslr/gogloader/internal/ui"
)

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <game>",
		Short: "Show available installers and parts",
		Long:  "Show available installers and parts.\n\n" + gameArgHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := gog.NewClient()
			if err != nil {
				return err
			}
			lp, err := c.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			prod, err := c.Product(cmd.Context(), lp.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			st := ui.For(out)
			printProduct(out, st, prod, "")
			if n := len(prod.ExpandedDLCs); n > 0 {
				fmt.Fprintf(out, "\n%s %s\n", st.Bold("DLC"), st.Dim(fmt.Sprintf("(%d)", n)))
				for i := range prod.ExpandedDLCs {
					fmt.Fprintln(out)
					printProduct(out, st, &prod.ExpandedDLCs[i], "  ")
				}
			}
			return nil
		},
	}
}

func printProduct(out io.Writer, st ui.Styler, p *gog.Product, indent string) {
	fmt.Fprintf(out, "%s%s\n", indent, st.Bold(p.Title))
	fmt.Fprintf(out, "%s%s\n", indent, st.Dim(fmt.Sprintf("id %d · slug %s", p.ID, p.Slug)))
	if len(p.Downloads.Installers) == 0 {
		fmt.Fprintf(out, "%s%s\n", indent, st.Dim("no installers"))
		return
	}
	installers := slices.Clone(p.Downloads.Installers)
	osOrder := map[string]int{"windows": 0, "mac": 1, "linux": 2}
	slices.SortStableFunc(installers, func(a, b gog.Installer) int {
		if c := cmp.Compare(osOrder[a.OS], osOrder[b.OS]); c != 0 {
			return c
		}
		return cmp.Compare(a.LanguageFull, b.LanguageFull)
	})
	fmt.Fprintln(out)
	t := &ui.Table{
		Header: []string{"OS", "LANGUAGE", "VERSION", "SIZE", "PARTS"},
		Align:  []ui.Align{ui.Left, ui.Left, ui.Left, ui.Right, ui.Right},
		Indent: indent + "  ",
	}
	for _, in := range installers {
		lang := in.LanguageFull
		if lang == "" {
			lang = in.Language
		}
		version := in.Version
		if version == "" {
			version = st.Dim("-")
		}
		t.Row(in.OS, lang, version, download.HumanBytes(in.TotalSize), fmt.Sprint(len(in.Files)))
	}
	t.Render(out, st)
}
