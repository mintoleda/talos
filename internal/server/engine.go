package server

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
)

// SlashHandler is called when the server receives a slash command from
// a client. The result string is broadcast as a Notice. emit can be used
// to broadcast additional events (e.g. ModelChanged).
type SlashHandler func(cmd string, emit func(protocol.Event)) string

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
	steer       serverSteerQueue

	slash     SlashHandler
	notifyCfg notify.Config

	// Live state for the benefit of newly-attached clients.
	stateMu    sync.Mutex
	stateBusy  bool
	stateText  string
	stateTools []protocol.ToolSnapshot
}

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
	lp.SteerFunc = e.steer.Drain
	go e.run()
	return e
}

// SetNotifyConfig sets the desktop notification configuration for events
// emitted during turn execution. Safe to call before the engine starts.
func (e *LoopEngine) SetNotifyConfig(cfg notify.Config) {
	e.notifyCfg = cfg
}

func (e *LoopEngine) SessionID() string { return e.session }

func (e *LoopEngine) SetSessionID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session = id
}

func (e *LoopEngine) Subscribe(fn func(protocol.Event)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribers = append(e.subscribers, fn)
}

func (e *LoopEngine) Emit(ev protocol.Event) {
	e.emit(ev)
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

func (e *LoopEngine) Steer(text string) {
	e.steer.Enqueue(text)
}

func (e *LoopEngine) WithdrawSteer() []protocol.ContentBlock {
	return e.steer.Withdraw()
}

// SetSlashHandler installs a handler for slash commands received from
// clients. Without one, slash commands are treated as ordinary user input.
func (e *LoopEngine) SetSlashHandler(h SlashHandler) {
	e.slash = h
}

type serverSteerQueue struct {
	mu       sync.Mutex
	messages []string
}

func (q *serverSteerQueue) Enqueue(text string) {
	q.mu.Lock()
	q.messages = append(q.messages, text)
	q.mu.Unlock()
}

func (q *serverSteerQueue) Drain() [][]protocol.ContentBlock {
	q.mu.Lock()
	msgs := q.messages
	q.messages = nil
	q.mu.Unlock()
	out := make([][]protocol.ContentBlock, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, protocol.TextBlocks(msg))
	}
	return out
}

func (q *serverSteerQueue) Withdraw() []protocol.ContentBlock {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.messages)
	if n == 0 {
		return nil
	}
	last := q.messages[n-1]
	q.messages = q.messages[:n-1]
	return protocol.TextBlocks(last)
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

// Snapshot returns the engine's current turn state so newly-attached clients
// can sync their UI (busy indicator, streamed text, active tools).
func (e *LoopEngine) Snapshot() protocol.EngineSnapshot {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	tools := make([]protocol.ToolSnapshot, len(e.stateTools))
	copy(tools, e.stateTools)
	return protocol.EngineSnapshot{
		Busy:         e.stateBusy,
		StreamedText: e.stateText,
		ActiveTools:  tools,
	}
}

// trackState returns an emit wrapper that updates the engine's live state
// as events flow through, so Snapshot() always returns current data.
func (e *LoopEngine) trackState(inner func(protocol.Event)) func(protocol.Event) {
	return func(ev protocol.Event) {
		switch evt := ev.(type) {
		case protocol.TextDelta:
			e.stateMu.Lock()
			e.stateText += evt.Text
			e.stateMu.Unlock()
		case protocol.ToolStarted:
			e.stateMu.Lock()
			e.stateTools = append(e.stateTools, protocol.ToolSnapshot{
				ID: evt.ID, Name: evt.Name, Args: evt.Args,
			})
			e.stateMu.Unlock()
		case protocol.ToolFinished:
			e.stateMu.Lock()
			for i, t := range e.stateTools {
				if t.ID == evt.ID {
					e.stateTools = append(e.stateTools[:i], e.stateTools[i+1:]...)
					break
				}
			}
			e.stateMu.Unlock()
		case protocol.TurnEnded:
			e.stateMu.Lock()
			e.stateBusy = false
			e.stateText = ""
			e.stateTools = nil
			e.stateMu.Unlock()
		}
		inner(ev)
	}
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

			// Mark the engine as busy and reset tracking state.
			e.stateMu.Lock()
			e.stateBusy = true
			e.stateText = ""
			e.stateTools = nil
			e.stateMu.Unlock()

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
			// Wrap with state tracking (before notify wrapping so state
			// is updated before notification dispatch).
			emit = e.trackState(emit)
			emit = notify.Wrap(emit, e.notifyCfg)
			if err := e.lp.RunTurn(turnCtx, protocol.TextBlocks(text), emit); err != nil && !errors.Is(err, context.Canceled) {
				e.emit(protocol.Notice{Level: "error", Text: err.Error()})
				e.emit(protocol.TurnEnded{})
			}
			cancel()
		}
	}
}
