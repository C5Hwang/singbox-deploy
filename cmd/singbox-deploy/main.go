package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/ui"
)

func main() {
	// The monitor subcommand runs the long-lived traffic monitor service and is
	// dispatched before the interactive UI. It is wired in the monitor task.
	if len(os.Args) > 1 && os.Args[1] == "monitor" {
		if err := runMonitor(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "monitor:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "cert" {
		if err := runCert(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "cert:", err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
