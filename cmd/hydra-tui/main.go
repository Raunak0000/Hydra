package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Raunak0000/Hydra/pkg/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	daemonURL := flag.String("daemon-url", "http://localhost:9000", "Hydra daemon URL")
	defaultToken := os.Getenv("HYDRA_API_TOKEN")
	daemonToken := flag.String("token", defaultToken, "Hydra daemon API token")
	flag.Parse()

	if *daemonToken == "" {
		fmt.Fprintln(os.Stderr, "Error: set HYDRA_API_TOKEN or provide --token")
		os.Exit(1)
	}

	client := tui.NewDaemonClient(*daemonURL, *daemonToken)

	program := tea.NewProgram(
		tui.NewModel(client),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hydra-tui: %v\n", err)
		os.Exit(1)
	}
}
