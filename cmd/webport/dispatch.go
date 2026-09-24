package main

import (
	"fmt"
	"io"
	"os"
)

// runCLI dispatches one invocation without terminating the process. Keeping
// streams at the boundary makes command parsing and inspection commands
// directly testable while main remains responsible for the process exit code.
func runCLI(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		runDaemon()
		return nil
	}

	switch args[0] {
	case "daemon":
		runDaemon()
		return nil
	case "version", "--version":
		_, err := fmt.Fprintln(out, versionString())
		return err
	case "help", "-h", "--help":
		return writeHelp(out)
	case "dev":
		return runDevWithIO(args[1:], in, out, errOut)
	case "route":
		return runRoute(args[1:])
	case "inspect":
		return runInspect(args[1:], out, errOut)
	case "list":
		return printEndpointTo(defaultAPI, "/routes", out)
	case "status":
		return printEndpointTo(defaultAPI, "/status", out)
	case "config":
		return printEndpointTo(defaultAPI, "/config", out)
	case "doctor":
		return runDoctorTo(out)
	case "dns":
		return runCompatibilityCommandTo("webport-dns", args[1:], in, out, errOut)
	case "install":
		return runInstallerTo(args[1:], in, out, errOut)
	case "upgrade":
		return runUpgradeTo(args[1:], in, out, errOut)
	default:
		_ = writeHelp(out)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// runCLIWithProcessStreams is the production adapter used by main.
func runCLIWithProcessStreams(args []string) error {
	return runCLI(args, os.Stdin, os.Stdout, os.Stderr)
}
