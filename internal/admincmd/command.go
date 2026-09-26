// ClawEh
// License: MIT

package admincmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/install"
)

// NewAdminCommand returns `claw admin [username]`: create or replace the one
// operator account the WebUI and API accept.
func NewAdminCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "admin [username]",
		Short: "Create or replace the WebUI admin account",
		Long: "Create the operator account for the WebUI and its API, or replace it. There is one\n" +
			"account; running the command again overwrites it. The password is asked for twice\n" +
			"without echo and must be at least " + strconv.Itoa(admin.MinPasswordLength) + " characters. The result is written to\n" +
			"<CLAW_HOME>/credentials.json (mode 0600); a running gateway picks it up within a\n" +
			"minute and signs everyone out. When stdin is not a terminal (a piped installer) the\n" +
			"prompts use /dev/tty; the password is never read from a pipe.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := ""
			if len(args) == 1 {
				username = args[0]
			}
			return run(cmd.OutOrStdout(), username)
		},
	}
}

// openTerminal is replaced in tests.
var openTerminal = admin.OpenTerminal

func run(out io.Writer, username string) (err error) {
	t, err := openTerminal(out)
	if err != nil {
		if errors.Is(err, admin.ErrNoTerminal) {
			return fmt.Errorf("claw admin needs an interactive terminal to read the password (%w)", err)
		}
		return err
	}
	defer func() {
		if cerr := t.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close terminal: %w", cerr)
		}
	}()

	username, password, err := admin.PromptAccount(t, username)
	if err != nil {
		return err
	}

	home, inst := ResolveHome()
	path := admin.Path(home)
	if err = admin.Write(path, username, password); err != nil {
		return err
	}
	if err = chownToService(path, inst); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Admin account %q written to %s\n", username, path)
	return err
}

// chownToService hands the file to the service account when root wrote it, so
// a gateway running as that account can read a 0600 file. Any other caller
// already owns the file.
func chownToService(path string, inst *install.ExistingInstall) error {
	if os.Geteuid() != 0 || inst == nil {
		return nil
	}
	name := inst.User
	if name == "" || name == "root" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("look up service user %q: %w", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("service user %q: uid %q: %w", name, u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("service user %q: gid %q: %w", name, u.Gid, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s to %s: %w", path, name, err)
	}
	return nil
}
