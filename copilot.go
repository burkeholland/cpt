package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

const systemPromptTemplate = `Translate the request into a safe, copy-pasteable %s command for %s.
Return exactly:
EXPLANATION: one short sentence
COMMAND: the best command
Add a second COMMAND only when it is a meaningfully different useful alternative.
Keep every command on one line. No markdown or extra prose.`

func systemPrompt(shell string) string {
	return fmt.Sprintf(systemPromptTemplate, shell, runtime.GOOS)
}

func reasoningEffort(modelName string) string {
	if modelName == defaultModel {
		return "none"
	}
	return ""
}

func rejectPermissionRequest(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
	feedback := "cpt only generates command text and does not allow tool execution"
	return &rpc.PermissionDecisionReject{Feedback: &feedback}, nil
}

type copilotClient struct {
	client  *copilot.Client
	models  []string
	mu      sync.Mutex
	started bool

	// Session reuse — kept alive across refinements
	session      *copilot.Session
	sessionModel string
	sessionShell string

	// Stream routing (protected by streamMu)
	streamMu     sync.Mutex
	activeStream *streamRequest
	activeCancel context.CancelFunc
}

func newCopilotClient() *copilotClient {
	return &copilotClient{}
}

type streamRequest struct {
	ctx     context.Context
	updates chan<- streamUpdate
	done    chan struct{}
	once    sync.Once
	final   atomic.Bool
}

func (r *streamRequest) send(update streamUpdate) {
	select {
	case r.updates <- update:
	case <-r.ctx.Done():
	}
}

func (r *streamRequest) sendDelta(delta string) {
	select {
	case r.updates <- streamUpdate{delta: delta}:
	default:
		// The final assistant message contains the complete response, so dropping
		// an intermediate delta is preferable to blocking the SDK event loop.
	}
}

func (r *streamRequest) complete(content string) {
	if r.final.CompareAndSwap(false, true) {
		r.send(streamUpdate{final: content, done: true})
	}
	r.finish()
}

func (r *streamRequest) fail(err error) {
	if r.final.CompareAndSwap(false, true) {
		r.send(streamUpdate{err: err})
	}
	r.finish()
}

func (r *streamRequest) finish() {
	r.once.Do(func() {
		close(r.done)
	})
}

func (c *copilotClient) start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	c.client = copilot.NewClient(&copilot.ClientOptions{
		LogLevel: "error",
	})
	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("failed to start copilot client: %w", err)
	}
	c.started = true
	return nil
}

func (c *copilotClient) stop() {
	c.cancelActiveStream()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		c.session.Disconnect()
		c.session = nil
	}
	if c.client != nil && c.started {
		c.client.Stop()
		c.started = false
	}
}

func (c *copilotClient) cancelActiveStream() {
	c.streamMu.Lock()
	cancel := c.activeCancel
	c.activeCancel = nil
	c.activeStream = nil
	c.streamMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (c *copilotClient) listModels(ctx context.Context) ([]string, error) {
	if err := c.start(ctx); err != nil {
		return nil, err
	}
	models, err := c.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	var names []string
	for _, m := range models {
		names = append(names, m.ID)
	}
	c.models = names
	return names, nil
}

type streamUpdate struct {
	delta string
	final string
	done  bool
	err   error
}

// ensureSession creates a session if one doesn't exist or if the model/shell changed.
// Must be called with c.mu held.
func (c *copilotClient) ensureSession(ctx context.Context, modelName, shell string) error {
	if c.session != nil && c.sessionModel == modelName && c.sessionShell == shell {
		return nil
	}

	// Disconnect stale session
	if c.session != nil {
		c.session.Disconnect()
		c.session = nil
	}

	session, err := c.client.CreateSession(ctx, &copilot.SessionConfig{
		Model:           modelName,
		ReasoningEffort: reasoningEffort(modelName),
		Streaming:       copilot.Bool(true),
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "replace",
			Content: systemPrompt(shell),
		},
		OnPermissionRequest: rejectPermissionRequest,
	})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	c.session = session
	c.sessionModel = modelName
	c.sessionShell = shell

	// Attach one event handler for the session lifetime and route events to the
	// currently active request. A request-local sync.Once protects duplicate
	// idle events from closing the same channel twice.
	session.On(func(event copilot.SessionEvent) {
		c.streamMu.Lock()
		active := c.activeStream
		c.streamMu.Unlock()

		if active == nil {
			return
		}

		switch d := event.Data.(type) {
		case *copilot.AssistantMessageDeltaData:
			active.sendDelta(d.DeltaContent)
		case *copilot.AssistantMessageData:
			active.complete(d.Content)
		case *copilot.SessionErrorData:
			active.fail(fmt.Errorf("copilot session error: %s", d.Message))
		case *copilot.SessionIdleData:
			active.finish()
		}
	})

	return nil
}

func (c *copilotClient) ask(ctx context.Context, prompt, modelName, shell string, updates chan<- streamUpdate) {
	defer close(updates)

	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := c.start(ctx); err != nil {
		updates <- streamUpdate{err: err}
		return
	}

	c.mu.Lock()
	if err := c.ensureSession(requestCtx, modelName, shell); err != nil {
		c.mu.Unlock()
		updates <- streamUpdate{err: err}
		return
	}

	request := &streamRequest{
		ctx:     requestCtx,
		updates: updates,
		done:    make(chan struct{}),
	}

	c.streamMu.Lock()
	c.activeStream = request
	c.activeCancel = cancel
	c.streamMu.Unlock()

	session := c.session
	c.mu.Unlock()

	defer func() {
		c.streamMu.Lock()
		if c.activeStream == request {
			c.activeStream = nil
			c.activeCancel = nil
		}
		c.streamMu.Unlock()
	}()

	_, err := session.Send(requestCtx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		request.send(streamUpdate{err: fmt.Errorf("failed to send message: %w", err)})
		return
	}

	select {
	case <-request.done:
		if !request.final.Load() {
			request.send(streamUpdate{done: true})
		}
	case <-requestCtx.Done():
	}
}
