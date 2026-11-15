package codex

import (
	"context"
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
	go parseStream(context.Background(), reader, out)
	if _, err := io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs\"}}\n\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"plan\"}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs\",\"summary\":[{\"text\":\"plan\"}]}}\n\n"); err != nil {
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
