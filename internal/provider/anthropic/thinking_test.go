package anthropic

import (
	"io"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestThinkingCompletionDoesNotWaitForNextToolOrStreamEnd(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	out := make(chan protocol.ProviderEvent, 16)
	go parseSSE(reader, out)
	if _, err := io.WriteString(writer, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"); err != nil {
		t.Fatal(err)
	}
	// Leave the stream open: completion must arrive before later tool arguments
	// or assistant text, rather than being inferred from a timeout or EOF.
	for i := 0; i < 2; i++ {
		select {
		case event := <-out:
			if i == 0 {
				if _, ok := event.(protocol.PEThinking); !ok {
					t.Fatalf("delta = %T", event)
				}
			} else if _, ok := event.(protocol.PEThinkingDone); !ok {
				t.Fatalf("completion = %T", event)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("thinking completion waited for more stream data")
		}
	}
}
