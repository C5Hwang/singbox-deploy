package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/ui"
)

var version = "dev"

func main() {
	ui.SetVersion(version)
	// The cert subcommand is the timer-triggered renewal entry point and is
	// dispatched before the interactive UI. The monitor service lives in
	// its own binary (singbox-monitor) so master and nodes share one image.
	if len(os.Args) > 1 && os.Args[1] == "cert" {
		if err := runCert(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "cert:", err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
