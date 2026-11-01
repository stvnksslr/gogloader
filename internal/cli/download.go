package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/download"
	"github.com/stvnksslr/gogloader/internal/gog"
	"github.com/stvnksslr/gogloader/internal/ui"
)

// fetchOpts are the flags shared by download and install.
type fetchOpts struct {
	os       string
	lang     string
	dir      string
	parallel int
	retries  int
	dlc      bool
}

func (o *fetchOpts) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.os, "os", "", "installer platform: windows, mac, linux (default: host OS, falling back to windows)")
	f.StringVar(&o.lang, "lang", "en", "installer language code")
	f.StringVarP(&o.dir, "dir", "d", "", "directory to download installers into (default: ./<slug>)")
	f.IntVarP(&o.parallel, "parallel", "p", 4, "number of parts to download concurrently")
	f.IntVar(&o.retries, "retries", 5, "attempts per part")
	f.BoolVar(&o.dlc, "dlc", false, "also include owned DLCs")
	_ = cmd.RegisterFlagCompletionFunc("os", cobra.FixedCompletions(
		[]string{"windows", "mac", "linux"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.MarkFlagDirname("dir")
}

func newDownloadCmd() *cobra.Command {
	var o fetchOpts
	cmd := &cobra.Command{
		Use:     "download <game>",
		Aliases: []string{"dl"},
		Short:   "Download all parts of an installer",
		Long:    "Download all parts of an installer (resumable, md5-verified).\n\n" + gameArgHelp,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fetch(cmd.Context(), args[0], o)
			return err
		},
	}
	o.register(cmd)
	return cmd
}

func hostOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

type fetchedItem struct {
	title string
	files []string
}

type fetchResult struct {
	slug     string
	platform string
	dir      string
	items    []fetchedItem
}

// fetch resolves a game and downloads its (and optionally its DLCs')
// installer parts.
func fetch(ctx context.Context, name string, o fetchOpts) (*fetchResult, error) {
	c, err := gog.NewClient()
	if err != nil {
		return nil, err
	}
	lp, err := c.Resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	prod, err := c.Product(ctx, lp.ID)
	if err != nil {
		return nil, err
	}

	// Pick the platform: explicit flag, else host OS, else windows.
	platform := o.os
	if platform == "" {
		platform = hostOS()
		if _, err := gog.SelectInstaller(prod.Downloads.Installers, platform, o.lang); err != nil {
			warnf("no %s build available, using the windows installer", platform)
			platform = "windows"
		}
	}

	products := []*gog.Product{prod}
	if o.dlc {
		for i := range prod.ExpandedDLCs {
			products = append(products, &prod.ExpandedDLCs[i])
		}
	}

	dl := &download.Downloader{
		Parallel: o.parallel,
		Retries:  o.retries,
		Client:   &http.Client{},
		Progress: os.Stderr,
	}
	dir := o.dir
	if dir == "" {
		dir = lp.Slug
	}
	res := &fetchResult{slug: lp.Slug, platform: platform, dir: dir}

	for _, p := range products {
		inst, err := gog.SelectInstaller(p.Downloads.Installers, platform, o.lang)
		if err != nil {
			if p != prod {
				warnf("skipping DLC %s: %v", p.Title, err)
				continue
			}
			return nil, fmt.Errorf("%s: %w", p.Title, err)
		}
		if !strings.EqualFold(inst.Language, o.lang) {
			warnf("%s: language %q not available, using %q", p.Title, o.lang, inst.Language)
		}
		st := ui.For(os.Stdout)
		lang := inst.LanguageFull
		if lang == "" {
			lang = inst.Language
		}
		meta := []string{inst.OS, lang}
		if inst.Version != "" {
			meta = append(meta, inst.Version)
		}
		meta = append(meta, download.HumanBytes(inst.TotalSize), ui.Plural(len(inst.Files), "part"))
		fmt.Printf("\n%s  %s\n", st.Bold(p.Title), st.Dim(strings.Join(meta, " · ")))

		parts := make([]download.Part, len(inst.Files))
		for i, f := range inst.Files {
			parts[i] = download.Part{
				Name: f.ID,
				Size: f.Size,
				Resolve: func(ctx context.Context) (download.Link, error) {
					l, err := c.ResolveFile(ctx, f)
					return download.Link{URL: l.URL, MD5: l.MD5}, err
				},
			}
		}
		files, err := dl.Fetch(ctx, res.dir, parts)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			fmt.Println(st.Dim("    " + f))
		}
		res.items = append(res.items, fetchedItem{title: p.Title, files: files})
	}
	return res, nil
}
