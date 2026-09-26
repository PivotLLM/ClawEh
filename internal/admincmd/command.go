// ClawEh
// License: MIT

package admincmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
			"minute and signs everyone out.",
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

func run(out io.Writer, username string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("claw admin needs an interactive terminal to read the password")
	}
	reader := bufio.NewReader(os.Stdin)

	username = strings.TrimSpace(username)
	if username == "" {
		if _, err := fmt.Fprint(out, "Username: "); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read username: %w", err)
		}
		username = strings.TrimSpace(line)
	}
	if err := validateUsername(username); err != nil {
		return err
	}

	password, err := readPassword(out, fd, "Password: ")
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(password) < admin.MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", admin.MinPasswordLength)
	}
	again, err := readPassword(out, fd, "Confirm password: ")
	if err != nil {
		return err
	}
	if password != again {
		return errors.New("passwords do not match")
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

func validateUsername(username string) error {
	if username == "" {
		return errors.New("username is required")
	}
	if utf8.RuneCountInString(username) > 64 {
		return errors.New("username must be at most 64 characters")
	}
	if strings.ContainsAny(username, " \t\r\n") {
		return errors.New("username must not contain whitespace")
	}
	return nil
}

func readPassword(out io.Writer, fd int, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	b, err := term.ReadPassword(fd)
	_, werr := fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if werr != nil {
		return "", werr
	}
	return string(b), nil
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
