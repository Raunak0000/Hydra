package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Raunak0000/Hydra/pkg/tui"
	tea "github.com/charmbracelet/bubbletea"
)

const defaultToken = "hydra_secure_token_bf1f753e"

func main() {
	daemonURL := flag.String("daemon-url", "http://localhost:9000", "Hydra daemon URL")
	daemonToken := flag.String("token", defaultToken, "Hydra daemon API token")
	flag.Parse()

	client := tui.NewDaemonClient(*daemonURL, *daemonToken)
	program := tea.NewProgram(tui.NewModel(client), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hydra-tui: %v\n", err)
		os.Exit(1)
	}
}
