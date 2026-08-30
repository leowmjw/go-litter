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
	if err := sessionapp.RunSession1CLI(ctx, os.Args[1:]); err != nil {
		slog.Error("session-1 stopped", "error", err)
		os.Exit(1)
	}
}
