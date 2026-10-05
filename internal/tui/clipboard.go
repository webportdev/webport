package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Copy uses the host clipboard when available. OSC 52 works over SSH and in
// terminals that support clipboard writes; the terminal decides whether to
// accept it, so the UI reports a request rather than claiming success.
func Copy(output io.Writer, value string) (string, error) {
	var candidates [][]string
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, []string{"pbcopy"})
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		candidates = append(candidates, []string{"wl-copy"})
	}
	if os.Getenv("DISPLAY") != "" {
		candidates = append(candidates, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		command := exec.CommandContext(ctx, path, candidate[1:]...)
		command.Stdin = strings.NewReader(value)
		err = command.Run()
		cancel()
		if err == nil {
			return "Copied to clipboard", nil
		}
	}
	_, err := fmt.Fprintf(output, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(value)))
	return "Clipboard request sent (terminal must support OSC 52)", err
}
