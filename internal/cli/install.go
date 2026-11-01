package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/install"
	"github.com/stvnksslr/gogloader/internal/ui"
)

func newInstallCmd() *cobra.Command {
	var (
		o       fetchOpts
		dest    string
		useWine bool
		keep    bool
	)
	home, _ := os.UserHomeDir()
	cmd := &cobra.Command{
		Use:   "install <game>",
		Short: "Download, then install/extract",
		Long:  "Download all parts of an installer, then install or extract it.\n\n" + gameArgHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := fetch(cmd.Context(), args[0], o)
			if err != nil {
				return err
			}
			st := ui.For(os.Stdout)
			target := filepath.Join(dest, res.slug)
			for _, r := range res.items {
				fmt.Printf("\n%s %s\n", st.Bold("Installing "+r.title), st.Dim("→ "+target))
				err := install.Install(cmd.Context(), install.Options{
					OS:      res.platform,
					Files:   r.files,
					Dest:    target,
					Log:     os.Stderr,
					UseWine: useWine,
				})
				if err != nil {
					return fmt.Errorf("install %s: %w", r.title, err)
				}
			}
			if !keep {
				for _, r := range res.items {
					for _, f := range r.files {
						_ = os.Remove(f)
					}
				}
				_ = os.Remove(res.dir) // only succeeds if empty
			}
			fmt.Printf("\n%s Installed to %s\n", st.Green("✓"), st.Bold(target))
			return nil
		},
	}
	o.register(cmd)
	f := cmd.Flags()
	f.StringVar(&dest, "dest", filepath.Join(home, "Games"), "install root (game goes into <dest>/<slug>)")
	f.BoolVar(&useWine, "wine", false, "run Windows installers under wine instead of extracting with innoextract")
	f.BoolVar(&keep, "keep", true, "keep downloaded installer files after installing")
	_ = cmd.MarkFlagDirname("dest")
	return cmd
}
