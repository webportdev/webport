package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/webportdev/webport/internal/socket"
	webuiserver "github.com/webportdev/webport/internal/webui"
)

const webUIUsage = `Usage:
  webport webui status [--format json]
  webport webui enable [--subdomain NAME]
  webport webui disable
  webport webui password [--stdin]
  webport webui subdomain [NAME | --reset]

The web UI is an optional, password-protected browser dashboard served by the
webport daemon. It has the same instances, routes, daemon status, services,
URLs, environment, logs, and stop/restart actions as "webport tui", and is
published at https://SUBDOMAIN.BASE_DOMAIN (default subdomain: webport).
These commands configure the daemon over its Unix socket.
`

// webUICall is replaced in tests.
var webUICall = func(ctx context.Context, operation string, payload, result any) error {
	return socket.Call(ctx, socket.DefaultPath(), operation, payload, result)
}

func callWebUI(operation string, payload any) (webuiserver.Report, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var report webuiserver.Report
	err := webUICall(ctx, operation, payload, &report)
	return report, err
}

func runWebUI(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status":
		return runWebUIStatus(args[1:], out, errOut)
	case "enable":
		return runWebUIEnable(args[1:], in, out, errOut)
	case "disable":
		return runWebUIDisable(args[1:], out, errOut)
	case "password":
		return runWebUIPassword(args[1:], in, out, errOut)
	case "subdomain":
		return runWebUISubdomain(args[1:], out, errOut)
	case "help", "-h", "--help":
		_, err := io.WriteString(out, webUIUsage)
		return err
	default:
		_, _ = io.WriteString(errOut, webUIUsage)
		return fmt.Errorf("unknown webui command %q", args[0])
	}
}

func parseWebUIFlags(name string, args []string, errOut io.Writer, define func(*flag.FlagSet)) (*flag.FlagSet, error) {
	flags := flag.NewFlagSet("webport webui "+name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	define(flags)
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return flags, nil
}

func writeWebUIReport(out io.Writer, report webuiserver.Report) error {
	yesNo := func(value bool) string {
		if value {
			return "yes"
		}
		return "no"
	}
	lines := []string{
		"enabled: " + yesNo(report.Enabled),
		"password set: " + yesNo(report.PasswordSet),
		"subdomain: " + report.Subdomain,
		"running: " + yesNo(report.Running),
	}
	if report.URL != "" {
		lines = append(lines, "url: "+report.URL)
	}
	if report.Listen != "" {
		lines = append(lines, "local: http://"+report.Listen)
	}
	if report.RouteError != "" {
		lines = append(lines, "route error: "+report.RouteError)
	}
	if report.SettingsPath != "" {
		lines = append(lines, "settings: "+report.SettingsPath)
	}
	_, err := io.WriteString(out, strings.Join(lines, "\n")+"\n")
	return err
}

func runWebUIStatus(args []string, out, errOut io.Writer) error {
	var format string
	flags, err := parseWebUIFlags("status", args, errOut, func(flags *flag.FlagSet) {
		flags.StringVar(&format, "format", "", "output format: json")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 0 || (format != "" && format != "json") {
		return errors.New("usage: webport webui status [--format json]")
	}
	report, err := callWebUI("webui.status", nil)
	if err != nil {
		return err
	}
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return writeWebUIReport(out, report)
}

func runWebUIEnable(args []string, in io.Reader, out, errOut io.Writer) error {
	var subdomain string
	flags, err := parseWebUIFlags("enable", args, errOut, func(flags *flag.FlagSet) {
		flags.StringVar(&subdomain, "subdomain", "", "hostname label (default: webport)")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("webport webui enable accepts no positional arguments")
	}
	update := webuiserver.Update{Enabled: ptr(true)}
	if subdomain != "" {
		subdomain = strings.ToLower(subdomain)
		if err := webuiserver.ValidateSubdomain(subdomain); err != nil {
			return err
		}
		update.Subdomain = &subdomain
	}
	current, err := callWebUI("webui.status", nil)
	if err != nil {
		return err
	}
	if !current.PasswordSet {
		password, err := promptNewPassword(in, errOut, "Set a password for the web UI.")
		if err != nil {
			return fmt.Errorf("%w (or set one first with: webport webui password --stdin)", err)
		}
		hash, err := webuiserver.HashPassword(password)
		if err != nil {
			return err
		}
		update.PasswordHash = &hash
	}
	report, err := callWebUI("webui.update", update)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Web UI enabled.")
	return writeWebUIReport(out, report)
}

func runWebUIDisable(args []string, out, errOut io.Writer) error {
	flags, err := parseWebUIFlags("disable", args, errOut, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("webport webui disable accepts no positional arguments")
	}
	if _, err := callWebUI("webui.update", webuiserver.Update{Enabled: ptr(false)}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Web UI disabled.")
	return err
}

func runWebUIPassword(args []string, in io.Reader, out, errOut io.Writer) error {
	var fromStdin bool
	flags, err := parseWebUIFlags("password", args, errOut, func(flags *flag.FlagSet) {
		flags.BoolVar(&fromStdin, "stdin", false, "read the new password from the first line of standard input")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("the password is never accepted as an argument; use the prompt or --stdin")
	}
	var password string
	if fromStdin {
		password, err = readPasswordLine(in)
	} else {
		password, err = promptNewPassword(in, errOut, "")
	}
	if err != nil {
		return err
	}
	// Hash locally so the cleartext never leaves this process.
	hash, err := webuiserver.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := callWebUI("webui.update", webuiserver.Update{PasswordHash: &hash}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Web UI password updated. Signed-in browsers must sign in again.")
	return err
}

func readPasswordLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password was provided on standard input")
	}
	return line, webuiserver.ValidatePassword(line)
}

func promptNewPassword(in io.Reader, errOut io.Writer, intro string) (string, error) {
	input, ok := in.(*os.File)
	if !ok || !term.IsTerminal(input.Fd()) {
		return "", errors.New("a terminal is required to enter a password; pipe it with --stdin instead")
	}
	if intro != "" {
		fmt.Fprintln(errOut, intro)
	}
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprintf(errOut, "New web UI password (at least %d characters): ", webuiserver.MinPasswordLength)
		first, err := term.ReadPassword(input.Fd())
		fmt.Fprintln(errOut)
		if err != nil {
			return "", err
		}
		if err := webuiserver.ValidatePassword(string(first)); err != nil {
			fmt.Fprintln(errOut, err)
			continue
		}
		fmt.Fprint(errOut, "Confirm password: ")
		second, err := term.ReadPassword(input.Fd())
		fmt.Fprintln(errOut)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			fmt.Fprintln(errOut, "Passwords do not match.")
			continue
		}
		return string(first), nil
	}
	return "", errors.New("password was not set")
}

func runWebUISubdomain(args []string, out, errOut io.Writer) error {
	var reset bool
	flags, err := parseWebUIFlags("subdomain", args, errOut, func(flags *flag.FlagSet) {
		flags.BoolVar(&reset, "reset", false, "restore the default subdomain ("+webuiserver.DefaultSubdomain+")")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 1 || (reset && flags.NArg() > 0) {
		return errors.New("usage: webport webui subdomain [NAME | --reset]")
	}
	if flags.NArg() == 0 && !reset {
		report, err := callWebUI("webui.status", nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, report.Subdomain)
		return err
	}
	value := ""
	if !reset {
		value = strings.ToLower(flags.Arg(0))
		if err := webuiserver.ValidateSubdomain(value); err != nil {
			return err
		}
	}
	report, err := callWebUI("webui.update", webuiserver.Update{Subdomain: &value})
	if err != nil {
		return err
	}
	message := "Web UI subdomain set to " + report.Subdomain
	if report.URL != "" {
		message += " (" + report.URL + ")"
	} else if report.RouteError != "" {
		message += "; route error: " + report.RouteError
	}
	_, err = fmt.Fprintln(out, message+".")
	return err
}

func ptr[T any](value T) *T { return &value }
