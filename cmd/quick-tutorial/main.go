package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"app/internal/sessionapp"
	"app/internal/tutorialserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if len(os.Args) > 1 && os.Args[1] == "tutorial" {
		err = tutorialserver.RunCLI(ctx, os.Args[2:])
	} else {
		// Default behavior: run the RamaSpace capstone server.
		err = sessionapp.RunQuickTutorialCLI(ctx, os.Args[1:])
	}

	if err != nil {
		slog.Error("quick-tutorial stopped", "error", err)
		os.Exit(1)
	}
}
