// Command talos-cache-parity-tui drives the daemon through the same client
// RPCs used by the Talos TUI and prints one engine.stats snapshot per turn.
// It is intentionally tiny: the parity harness owns the scripted input, but
// Talos remains the owner of prompt construction and cache accounting.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/server"
)

func main() {
	baseDir := flag.String("base-dir", "", "Talos home containing daemon.json")
	dir := flag.String("dir", "", "project directory for the scripted session")
	flag.Parse()
	if *baseDir == "" || *dir == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: talos-cache-parity-tui --base-dir <talos-home> --dir <project> <turn>...")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	discovery, err := server.ReadDiscovery(server.DiscoveryPath(*baseDir))
	if err != nil {
		fatal(err)
	}
	conn, events, err := client.RunClientNetwork(ctx, "unix", discovery.Socket, discovery.Token)
	if err != nil {
		fatal(err)
	}

	createRaw, err := conn.Request(ctx, rpc.DaemonCreateSession, rpc.CreateSessionParams{
		Dir:       *dir,
		Isolation: "none",
		Model:     "parity-model",
		Provider:  "openai",
	})
	if err != nil {
		fatal(err)
	}
	var created rpc.CreateSessionResult
	if err := json.Unmarshal(createRaw, &created); err != nil {
		fatal(err)
	}
	if created.Session.ID == "" {
		fatal(fmt.Errorf("daemon returned an empty session id"))
	}
	conn.Session = created.Session.ID
	if err := conn.Subscribe(created.Session.ID); err != nil {
		fatal(err)
	}

	for _, input := range flag.Args() {
		if err := conn.Send(input); err != nil {
			fatal(err)
		}
		if err := waitForTurn(ctx, events); err != nil {
			fatal(err)
		}
		statsRaw, err := conn.Request(ctx, rpc.Stats, nil)
		if err != nil {
			fatal(err)
		}
		var stats rpc.StatsResult
		if err := json.Unmarshal(statsRaw, &stats); err != nil {
			fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(stats); err != nil {
			fatal(err)
		}
	}
}

func waitForTurn(ctx context.Context, events <-chan protocol.Event) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("daemon event stream closed before TurnEnded")
			}
			switch value := event.(type) {
			case protocol.TurnEnded:
				return nil
			case protocol.Notice:
				if value.Level == "error" {
					return fmt.Errorf("talos turn failed: %s", value.Text)
				}
			}
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
