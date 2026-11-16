package cli

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui"
)

// uiCmd runs the local web UI: the same backend as the terminal UI, served
// to a browser on this computer only.
func (a *app) uiCmd() *cobra.Command {
	var noOpen bool
	var port int
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open Golden Gate in your browser (on this computer only)",
		Long: `Starts a small web server on this computer and opens it in your browser.

It listens on 127.0.0.1 only, on a port the system picks, and runs until you
press Ctrl-C. The link carries a single-use code that expires in two minutes;
press Enter here for a new link. The page can do exactly what the terminal UI
and the commands can do, and nothing is sent by opening it.

  --no-open   print the link instead of opening a browser (for SSH port forwarding)
  --port N    use a fixed port`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			srv, err := webui.New(a.tuiBackend(), webui.Options{Port: port, Now: a.Now})
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(c.Context())
			defer cancel()
			link := srv.LaunchURL()
			fmt.Fprintf(a.Stdout, "Golden Gate is running at %s (this computer only).\nOpen this link within two minutes; it works once:\n\n    %s\n\nPress Enter for a new link, Ctrl-C to stop.\n", srv.Origin(), link)
			if !noOpen && a.OpenBrowser != nil {
				if err := a.OpenBrowser(link); err != nil {
					fmt.Fprintln(a.Stderr, "could not open a browser; open the link above yourself")
				}
			}
			if a.Interactive && a.Stdin != nil {
				go func() {
					sc := bufio.NewScanner(a.Stdin)
					for sc.Scan() {
						fmt.Fprintf(a.Stdout, "New link (works once, for two minutes):\n\n    %s\n\n", srv.LaunchURL())
					}
				}()
			}
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link; do not open a browser")
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (default: one the system picks)")
	return cmd
}

// openBrowser hands a URL to the system's opener. The URL is passed as an
// argument, never through a shell.
func openBrowser(url string) error {
	ctx := context.Background() // the opener outlives this call; it must not be cancelled with it
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", url).Start() //nolint:gosec // G204: a fixed program; the URL is our own loopback link
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:gosec // G204: as above
	}
	return exec.CommandContext(ctx, "xdg-open", url).Start() //nolint:gosec // G204: as above
}
