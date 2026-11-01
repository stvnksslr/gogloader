// Package cli implements the gogloader command tree.
package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/gog"
)

const gameArgHelp = `<game> is a store slug (the_witcher_3_wild_hunt), a store URL
(https://www.gog.com/en/game/the_witcher_3_wild_hunt), a product id or a title.`

// NewRootCmd builds the gogloader command tree.
func NewRootCmd(info BuildInfo) *cobra.Command {
	info = info.resolve()
	gog.Warnf = warnf
	gog.UserAgent = "gogloader/" + info.Version
	root := &cobra.Command{
		Use:           "gogloader",
		Version:       info.String(),
		Short:         "Download & install GOG.com games by store name",
		Long:          "Download & install GOG.com games (including multipart installers) by store name.\n\n" + gameArgHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newAuthCmd(),
		newListCmd(),
		newInfoCmd(),
		newDownloadCmd(),
		newInstallCmd(),
	)
	root.SetVersionTemplate("gogloader {{.Version}}\n")
	return root
}

// Execute runs the CLI with os.Args.
func Execute(ctx context.Context, info BuildInfo) error {
	return NewRootCmd(info).ExecuteContext(ctx)
}
