package main

import (
	"embed"
	"log"
	"log/slog"

	"tusk/internal/csvexport"
	"tusk/internal/database"
	"tusk/internal/db"
	"tusk/internal/postgres"
	"tusk/internal/sqlite"
	"tusk/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	database := database.NewService(map[string]db.Provider{"postgres": postgres.NewProvider(), "sqlite": sqlite.NewProvider()})
	connections := store.NewConnectionsService(database)
	app := application.New(application.Options{
		Name:        "Tusk",
		Description: "A lightweight PostgreSQL and SQLite desktop client",
		Services: []application.Service{
			application.NewService(database),
			application.NewService(connections),
			application.NewService(csvexport.NewService()),
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
		Title:            "Tusk",
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
