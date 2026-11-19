package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(adminCmd)
	adminCmd.AddCommand(setupLinkCmd)
}

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Manage the web admin console",
}

var errSetupLinkRemote = errors.New("run this on the orgscale server: setup links are only made over the local socket")

var setupLinkCmd = &cobra.Command{
	Use:   "setup-link",
	Short: "Make a one-time link to set up single sign-on in the admin console",
	Long: `Makes a link that opens the admin console's setup page, where single
sign-on is configured and the first administrator is named. The link works
once and expires after 30 minutes; making a new one replaces the old.

Use it on a new installation, or to recover when nobody can sign in, for
example after the identity provider changed. It only works on the server
itself, over the local socket.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := loaded.Config
		if cfg.CLI.Address != "" {
			return errSetupLinkRemote
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), cfg.CLI.Timeout)
		defer cancel()

		client := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialOrgscaleSocket(ctx, cfg.UnixSocket)
			},
		}}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/orgscale/setup-link", nil)
		if err != nil {
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connecting to orgscale: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return errors.New("the admin console is not enabled on this server (admin.enabled), or it needs HTTPS (see the server log)")
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("orgscale answered %s", resp.Status)
		}

		var out struct {
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expiresAt"`
		}

		err = json.NewDecoder(resp.Body).Decode(&out)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Open this link within 30 minutes (until %s). It works once:\n\n  %s\n",
			out.ExpiresAt.Local().Format("15:04 MST"), out.URL)

		return nil
	},
}
