package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Raunak0000/Hydra/pkg/storage"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

func main() {
	app := NewApp()

	// 1. Initialize SQLite Database Engine
	dbStore, err := storage.GetDBStore()
	if err != nil {
		log.Fatalf("[X] Critical: Failed to initialize SQLite storage: %v", err)
	}
	defer dbStore.Close()

	// 2. Initialize Queue Manager & IPC Server
	storage.InitQueueManager(2, func(url string, savePath string, jobID string, headers map[string]string) {
		// Download job trigger execution stub
	})

	go storage.StartIPCServer(func(url string, savePath string, jobID string) {
		// IPC execution stub
	})

	// 3. Create your existing Server router
	server := storage.NewServer(func(url string, savePath string, jobID string, headers map[string]string) {})

	// 4. Run Wails window pointing directly to your Go server router handler
	err = wails.Run(&options.App{
		Title:  "Hydra Download Manager",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Handler: server.Router, // Routes all UI requests directly to your templ server!
		},
		OnStartup: app.startup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatal("Error running Wails application:", err)
	}

	// Graceful shutdown cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	_ = os.Remove(storage.GetSocketPath())
}
