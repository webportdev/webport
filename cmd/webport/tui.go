package main

import (
	"errors"
	"flag"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/webportdev/webport/internal/tui"
)

func runTUI(args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("webport tui", flag.ContinueOnError)
	flags.SetOutput(errOut)
	api := flags.String("api", defaultAPI, "webport daemon API URL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) > 0 {
		return errors.New("webport tui accepts no positional arguments")
	}
	input, ok := in.(*os.File)
	if !ok || !term.IsTerminal(input.Fd()) {
		return errors.New("webport tui requires an interactive terminal; use webport list or webport dev status for scripts")
	}
	return tui.Run(in, out, tui.LocalBackend{API: *api})
}
