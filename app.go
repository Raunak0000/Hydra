package main

import (
	"context"

	"github.com/Raunak0000/Hydra/pkg/storage"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// SelectDirectory exposes the native OS file picker to the frontend JS layer
func (a *App) SelectDirectory(defaultPath string) (string, error) {
	selected, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Hydra: Choose Save Directory",
		DefaultDirectory: defaultPath,
	})
	if err == nil && selected != "" {
		return selected, nil
	}

	// Fallback to existing storage path helper if native dialog is canceled
	return storage.ChooseFolderDialog(defaultPath)
}
