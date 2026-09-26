// ClawEh
// License: MIT

package admin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Environment variables that create the account without a prompt, for an
// unattended install. Both must be set; they are read only when no account
// exists yet.
const (
	EnvUser     = "CLAW_ADMIN_USER"
	EnvPassword = "CLAW_ADMIN_PASSWORD"
)

// maxAttempts bounds how often an interactive prompt re-asks after an invalid
// username or a short or mismatched password.
const maxAttempts = 3

// ErrNoTerminal means neither stdin nor /dev/tty is a terminal, so a password
// cannot be read without echo.
var ErrNoTerminal = errors.New("no terminal to read the password from")

// ErrNoAccountNoTerminal is what EnsureAccount returns when there is no
// account and no way to create one; the text says how to proceed.
var ErrNoAccountNoTerminal = errors.New("no admin account and no terminal to create one: " +
	"export " + EnvUser + " and " + EnvPassword + ", or run `claw admin` on the server, then rerun the installer")

// Terminal is where the account prompts are read and written.
type Terminal struct {
	In           io.Reader              // the username line
	Out          io.Writer              // prompts and messages
	ReadPassword func() ([]byte, error) // one line, without echo
	Close        func() error
}

// OpenTerminal returns stdin when it is a terminal, with prompts on out.
// Otherwise it opens /dev/tty for both, which is the case under
// `curl … | bash`, where stdin is the script. It returns ErrNoTerminal
// (wrapped) when neither is available; a password is never read from a pipe.
func OpenTerminal(out io.Writer) (*Terminal, error) {
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		return &Terminal{
			In:           os.Stdin,
			Out:          out,
			ReadPassword: func() ([]byte, error) { return term.ReadPassword(fd) },
			Close:        func() error { return nil },
		}, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: stdin is not a terminal and /dev/tty: %w", ErrNoTerminal, err)
	}
	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		_ = tty.Close() //nolint:errcheck // already failing; the close error adds nothing
		return nil, fmt.Errorf("%w: /dev/tty is not a terminal", ErrNoTerminal)
	}
	return &Terminal{
		In:           tty,
		Out:          tty,
		ReadPassword: func() ([]byte, error) { return term.ReadPassword(fd) },
		Close:        tty.Close,
	}, nil
}

// ValidateUsername applies the account's username rules.
func ValidateUsername(username string) error {
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

// ValidatePassword applies the minimum length.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

// PromptAccount asks on t for the username, unless one is given, and for the
// password twice without echo. A prompted username that breaks the rules, and
// a password that is too short or not repeated exactly, are asked for again,
// up to three times. A username passed in is validated but not re-asked.
func PromptAccount(t *Terminal, username string) (string, string, error) {
	username = strings.TrimSpace(username)
	if username != "" {
		if err := ValidateUsername(username); err != nil {
			return "", "", err
		}
	} else {
		var err error
		if username, err = promptUsername(t); err != nil {
			return "", "", err
		}
	}
	password, err := promptPassword(t)
	if err != nil {
		return "", "", err
	}
	return username, password, nil
}

func promptUsername(t *Terminal) (string, error) {
	reader := bufio.NewReader(t.In)
	var lastErr error
	for range maxAttempts {
		if _, err := fmt.Fprint(t.Out, "Username: "); err != nil {
			return "", err
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read username: %w", err)
		}
		name := strings.TrimSpace(line)
		lastErr = ValidateUsername(name)
		if lastErr == nil {
			return name, nil
		}
		if errors.Is(err, io.EOF) {
			return "", lastErr // nothing more to read; asking again would spin
		}
		if _, werr := fmt.Fprintln(t.Out, lastErr); werr != nil {
			return "", werr
		}
	}
	return "", lastErr
}

func promptPassword(t *Terminal) (string, error) {
	var lastErr error
	for range maxAttempts {
		password, err := readSecret(t, "Password: ")
		if err != nil {
			return "", err
		}
		if lastErr = ValidatePassword(password); lastErr == nil {
			again, rerr := readSecret(t, "Confirm password: ")
			if rerr != nil {
				return "", rerr
			}
			if password == again {
				return password, nil
			}
			lastErr = errors.New("passwords do not match")
		}
		if _, werr := fmt.Fprintln(t.Out, lastErr); werr != nil {
			return "", werr
		}
	}
	return "", lastErr
}

func readSecret(t *Terminal, prompt string) (string, error) {
	if _, err := fmt.Fprint(t.Out, prompt); err != nil {
		return "", err
	}
	b, err := t.ReadPassword()
	_, werr := fmt.Fprintln(t.Out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if werr != nil {
		return "", werr
	}
	return string(b), nil
}

// EnsureOptions configures EnsureAccount. Home is required; the rest default
// to the process environment and terminal.
type EnsureOptions struct {
	Home string // CLAW_HOME the service reads

	// Getenv reads EnvUser and EnvPassword; nil means os.Getenv.
	Getenv func(string) string
	// OpenTerminal opens the prompt terminal; nil means OpenTerminal.
	OpenTerminal func(out io.Writer) (*Terminal, error)
	// Out receives prompts when stdin is the terminal; nil means os.Stdout.
	Out io.Writer
	// Chown, when set, hands the written file to the service account. If it
	// fails the file is removed, so a rerun asks again instead of finding an
	// account the service cannot read.
	Chown func(path string) error
}

// EnsureAccount makes sure <Home>/credentials.json holds an account and
// returns its username. An existing file that loads is left untouched
// (created is false); one that exists but does not load is an error, never
// overwritten. Otherwise the account comes from EnvUser and EnvPassword when
// both are set, else from an interactive prompt. With neither it returns
// ErrNoAccountNoTerminal.
func EnsureAccount(opts EnsureOptions) (username string, created bool, err error) {
	if opts.Home == "" {
		return "", false, errors.New("admin account: CLAW_HOME is empty")
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	open := opts.OpenTerminal
	if open == nil {
		open = OpenTerminal
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	path := Path(opts.Home)

	creds, err := Load(path)
	switch {
	case err == nil:
		return creds.Username, false, nil
	case !errors.Is(err, ErrNotConfigured):
		return "", false, fmt.Errorf("existing admin account unusable: %w (fix it, or replace it with `claw admin`)", err)
	}

	envUser, envPass := getenv(EnvUser), getenv(EnvPassword)
	var password string
	switch {
	case envUser != "" && envPass != "":
		username = strings.TrimSpace(envUser)
		if verr := ValidateUsername(username); verr != nil {
			return "", false, fmt.Errorf("%s: %w", EnvUser, verr)
		}
		if verr := ValidatePassword(envPass); verr != nil {
			return "", false, fmt.Errorf("%s: %w", EnvPassword, verr)
		}
		password = envPass
	case envUser != "" || envPass != "":
		return "", false, fmt.Errorf("set both %s and %s, or neither to be prompted", EnvUser, EnvPassword)
	default:
		if username, password, err = promptOnTerminal(open, out, path); err != nil {
			return "", false, err
		}
	}

	if err := Write(path, username, password); err != nil {
		return "", false, err
	}
	if opts.Chown != nil {
		if err := opts.Chown(path); err != nil {
			if rmErr := os.Remove(path); rmErr != nil {
				return "", false, fmt.Errorf("%w (and removing %s failed: %w)", err, path, rmErr)
			}
			return "", false, err
		}
	}
	return username, true, nil
}

// promptOnTerminal opens the terminal, explains why it is asking and prompts
// for a new account.
func promptOnTerminal(open func(io.Writer) (*Terminal, error), out io.Writer, path string) (username, password string, err error) {
	t, err := open(out)
	if err != nil {
		if errors.Is(err, ErrNoTerminal) {
			return "", "", ErrNoAccountNoTerminal
		}
		return "", "", err
	}
	defer func() {
		if cerr := t.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close terminal: %w", cerr)
		}
	}()
	if _, err = fmt.Fprintf(t.Out, "\nThe WebUI needs an admin login and there is none yet (%s).\n"+
		"Create it now; the password must be at least %d characters.\n", path, MinPasswordLength); err != nil {
		return "", "", err
	}
	if username, password, err = PromptAccount(t, ""); err != nil {
		return "", "", fmt.Errorf("admin account: %w", err)
	}
	return username, password, nil
}
