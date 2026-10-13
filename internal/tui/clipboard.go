package tui

import (
	"encoding/base64"
	"fmt"
	"io"
)

// Copy sends an OSC 52 clipboard request to the terminal, including over SSH.
// The terminal decides whether to accept it, so the UI reports a request
// rather than claiming success.
func Copy(output io.Writer, value string) (string, error) {
	_, err := fmt.Fprintf(output, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(value)))
	return "Clipboard request sent (terminal must support OSC 52)", err
}
