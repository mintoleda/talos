package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mintoleda/talos/internal/acp"
	"github.com/mintoleda/talos/internal/config"
)

// runACP serves the Agent Client Protocol on stdio so Paseo (or any ACP
// client) can drive Talos headlessly. The client owns the UI, workspace and
// permission prompts; Talos owns the loop, tools and transcript.
func runACP(cfg *config.Config) error {
	// Stdout carries the protocol. Point os.Stdout at stderr so any stray
	// print from the runtime cannot corrupt the stream.
	out := os.Stdout
	os.Stdout = os.Stderr

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// Unblock the stdin reader so sessions close cleanly; force exit if
		// shutdown stalls.
		_ = os.Stdin.Close()
		time.AfterFunc(3*time.Second, func() { os.Exit(1) })
	}()
	return acp.Serve(ctx, cfg, os.Stdin, out)
}
