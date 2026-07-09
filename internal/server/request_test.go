package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client"
)

func TestClientRequestEcho(t *testing.T) {
	s, cancel := startRequestServer(t, func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method != "engine.echo" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return params, nil
	})
	defer cancel()

	ctx, cancelReq := context.WithTimeout(context.Background(), time.Second)
	defer cancelReq()
	got, err := s.Request(ctx, "engine.echo", map[string]string{"text": "hello"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if string(got) != `{"text":"hello"}` {
		t.Fatalf("got %s", got)
	}
}

func TestClientRequestConcurrent(t *testing.T) {
	s, cancel := startRequestServer(t, func(_ context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		time.Sleep(time.Duration(5-p.N%5) * time.Millisecond)
		return json.Marshal(p)
	})
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancelReq := context.WithTimeout(context.Background(), time.Second)
			defer cancelReq()
			raw, err := s.Request(ctx, "engine.echo", map[string]int{"n": i})
			if err != nil {
				errs <- err
				return
			}
			var got struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				errs <- err
				return
			}
			if got.N != i {
				errs <- fmt.Errorf("got %d, want %d", got.N, i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestClientRequestCancelUnblocks(t *testing.T) {
	release := make(chan struct{})
	s, cancel := startRequestServer(t, func(_ context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
		<-release
		return params, nil
	})
	defer cancel()
	defer close(release)

	ctx, cancelReq := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelReq()
	if _, err := s.Request(ctx, "engine.slow", map[string]string{"text": "late"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestServerRequestUnknownWithoutHandler(t *testing.T) {
	s, cancel := startRequestServer(t, nil)
	defer cancel()

	ctx, cancelReq := context.WithTimeout(context.Background(), time.Second)
	defer cancelReq()
	if _, err := s.Request(ctx, "engine.missing", nil); err == nil {
		t.Fatal("expected unknown method error")
	}
}

func startRequestServer(t *testing.T, h RequestHandler) (*client.ClientConn, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "request.sock")
	pidPath := filepath.Join(dir, "request.pid")

	eng := &testEngine{sessionID: "request-session"}
	srv := New(eng, sockPath, pidPath, 0)
	if h != nil {
		srv.SetRequestHandler(h)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = srv.Start(ctx)
	}()

	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	conn, _, err := client.RunClient(context.Background(), sockPath)
	if err != nil {
		cancel()
		t.Fatalf("run client: %v", err)
	}
	return conn, cancel
}
