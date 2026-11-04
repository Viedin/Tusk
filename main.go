package main

import (
	"embed"
	"log"
	"log/slog"

	"tusk/internal/postgres"
	"tusk/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	database := postgres.NewService()
	connections := store.NewConnectionsService(database)
	app := application.New(application.Options{
		Name:        "Tusk",
		Description: "A lightweight PostgreSQL desktop client",
		Services: []application.Service{
			application.NewService(database),
			application.NewService(connections),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		LogLevel: slog.LevelWarn,
		OnShutdown: func() {
			database.Shutdown()
			connections.Shutdown()
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Tusk — PostgreSQL",
		Width:            1240,
		Height:           800,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(255, 255, 255),
		URL:              "/",
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
