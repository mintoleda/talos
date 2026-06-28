package server

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
)

// SlashHandler is called when the server receives a slash command from
// a client. The result string is broadcast as a Notice. emit can be used
// to broadcast additional events (e.g. ModelChanged).
type SlashHandler func(cmd string, emit func(protocol.Event)) string

// LoopEngine wraps a loop.Loop and safety.Checkpointer into the Engine
// interface used by the server transport.
type LoopEngine struct {
	lp      *loop.Loop
	cp      *safety.Checkpointer
	session string

	mu           sync.Mutex
	subscribers  []func(protocol.Event)
	pendingReply func(bool, []byte)

	inputCh     chan string
	interruptCh chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc

	slash SlashHandler
}

// NewLoopEngine starts the engine. The sessionID is used in the hello message.
func NewLoopEngine(parentCtx context.Context, lp *loop.Loop, cp *safety.Checkpointer, sessionID string) *LoopEngine {
	ctx, cancel := context.WithCancel(parentCtx)
	e := &LoopEngine{
		lp:          lp,
		cp:          cp,
		session:     sessionID,
		inputCh:     make(chan string, 1),
		interruptCh: make(chan struct{}, 1),
		ctx:         ctx,
		cancel:      cancel,
	}
	go e.run()
	return e
}

func (e *LoopEngine) SessionID() string { return e.session }

func (e *LoopEngine) Subscribe(fn func(protocol.Event)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribers = append(e.subscribers, fn)
}

func (e *LoopEngine) emit(ev protocol.Event) {
	e.mu.Lock()
	subs := make([]func(protocol.Event), len(e.subscribers))
	copy(subs, e.subscribers)
	e.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (e *LoopEngine) Submit(text string) {
	select {
	case e.inputCh <- text:
	case <-e.ctx.Done():
	}
}

func (e *LoopEngine) Interrupt() {
	select {
	case e.interruptCh <- struct{}{}:
	case <-e.ctx.Done():
	}
}

// SetSlashHandler installs a handler for slash commands received from
// clients. Without one, slash commands are treated as ordinary user input.
func (e *LoopEngine) SetSlashHandler(h SlashHandler) {
	e.slash = h
}

func (e *LoopEngine) Approve(approved bool, plan []byte) {
	e.mu.Lock()
	pending := e.pendingReply
	e.pendingReply = nil
	e.mu.Unlock()
	if pending != nil {
		pending(approved, plan)
	}
}

func (e *LoopEngine) setPending(fn func(bool, []byte)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingReply = fn
}

func (e *LoopEngine) run() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case text := <-e.inputCh:
			// Slash commands (/model, /thinking, …) are handled by the
			// server rather than submitted as user input.
			if strings.HasPrefix(text, "/") && e.slash != nil {
				result := e.slash(text, e.emit)
				e.emit(protocol.Notice{Level: "info", Text: result})
				continue
			}
			// Broadcast the user's input to all attached clients so they
			// can render it — not just the submitting client.
			e.emit(protocol.UserInput{Text: text})
			if e.cp != nil {
				_, _ = e.cp.Snapshot("before-run")
			}
			turnCtx, cancel := context.WithCancel(e.ctx)
			go func() {
				select {
				case <-e.interruptCh:
					cancel()
				case <-turnCtx.Done():
				}
			}()
			emit := func(ev protocol.Event) {
				// Intercept permission/plan/merge requests so the Approve method
				// can reply to the original channel owned by the executor.
				switch evt := ev.(type) {
				case protocol.PermissionRequested:
					e.setPending(func(approved bool, _ []byte) {
						if evt.ReplyCh != nil {
							evt.ReplyCh <- approved
						}
					})
				}
				e.emit(ev)
			}
			if err := e.lp.RunTurn(turnCtx, protocol.TextBlocks(text), emit); err != nil && !errors.Is(err, context.Canceled) {
				e.emit(protocol.Notice{Level: "error", Text: err.Error()})
				e.emit(protocol.TurnEnded{})
			}
			cancel()
		}
	}
}
