package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stvnksslr/gogloader/internal/gog"
	"github.com/stvnksslr/gogloader/internal/ui"
)

func newAuthCmd() *cobra.Command {
	status := &cobra.Command{
		Use:   "status",
		Short: "Show login status",
		Args:  cobra.NoArgs,
		RunE:  runAuthStatus,
	}
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage GOG login (shows status when run without a subcommand)",
		Args:  cobra.NoArgs,
		RunE:  runAuthStatus,
	}
	auth.AddCommand(
		status,
		&cobra.Command{
			Use:   "login",
			Short: "Authenticate with GOG via the browser (one time)",
			Args:  cobra.NoArgs,
			RunE:  runAuthLogin,
		},
		&cobra.Command{
			Use:   "logout",
			Short: "Forget stored credentials",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := gog.Logout(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), ui.For(cmd.OutOrStdout()).Green("✓")+" Logged out.")
				return nil
			},
		},
	)
	return auth
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	c, err := gog.NewClient()
	if errors.Is(err, gog.ErrNotLoggedIn) {
		st := ui.For(out)
		fmt.Fprintf(out, "%s Not logged in. Run %s.\n", st.Yellow("!"), st.Bold("gogloader auth login"))
		return nil
	}
	if err != nil {
		return err
	}
	u, err := c.User(cmd.Context())
	if err != nil {
		return fmt.Errorf("stored token in %s is not usable: %w", gog.TokenLocation(), err)
	}
	st := ui.For(out)
	fmt.Fprintf(out, "%s Logged in as %s\n\n", st.Green("✓"), st.Bold(u.Username))
	t := &ui.Table{Indent: "  "}
	if u.Email != "" {
		t.Row(st.Dim("email"), u.Email)
	}
	t.Row(st.Dim("user id"), u.UserID)
	t.Row(st.Dim("stored in"), gog.TokenLocation())
	t.Row(st.Dim("expires"), c.TokenExpiry().Local().Format("2006-01-02 15:04")+" "+st.Dim("(refreshed automatically)"))
	t.Render(out, st)
	return nil
}

func runAuthLogin(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "1. Open this URL in your browser and log in to GOG:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "   "+gog.LoginURL())
	fmt.Fprintln(out)
	fmt.Fprintln(out, "2. You will land on a blank page at embed.gog.com/on_login_success?...code=...")
	fmt.Fprintln(out, "   Copy that full URL (or just the code) and paste it here.")
	fmt.Fprintln(out)
	fmt.Fprint(out, "URL or code: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	code, err := gog.ExtractCode(line)
	if err != nil {
		return err
	}
	if _, err := gog.Login(cmd.Context(), code); err != nil {
		return err
	}
	st := ui.For(out)
	fmt.Fprintf(out, "%s Logged in. Token stored in %s\n", st.Green("✓"), gog.TokenLocation())
	return nil
}
