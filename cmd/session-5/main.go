package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"app/internal/sessionapp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sessionapp.RunSession5CLI(ctx, os.Args[1:]); err != nil {
		log.Printf("session-5: %v", err)
		os.Exit(1)
	}
}
