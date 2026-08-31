package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"app/internal/sessionapp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sessionapp.RunQuickTutorialCLI(ctx, os.Args[1:]); err != nil {
		slog.Error("quick-tutorial stopped", "error", err)
		os.Exit(1)
	}
}
