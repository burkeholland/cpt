package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
)

const systemPromptTemplate = `You translate natural language into shell commands.

Output format — follow this exactly:
1. First, print one short explanation line starting with "EXPLANATION:" (max ~15 words)
2. Then print one or more command lines, each starting with "COMMAND:"
3. If there are multiple alternative ways, list the best 2-3 as separate COMMAND: lines
4. Each command must be a valid, copy-pasteable shell command for %s on %s
5. No other output — no markdown, no code fences, no numbering, no extra prose

Example input:  "kill process on port 3000"
Example output (bash/zsh):
EXPLANATION: Kill any process listening on port 3000
COMMAND: lsof -ti:3000 | xargs kill -9
COMMAND: fuser -k 3000/tcp
Example output (PowerShell):
EXPLANATION: Terminate the process using port 3000
COMMAND: Stop-Process -Id (Get-NetTCPConnection -LocalPort 3000).OwningProcess -Force`

func systemPrompt(shell string) string {
	return fmt.Sprintf(systemPromptTemplate, shell, runtime.GOOS)
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
	streamMu       sync.Mutex
	currentUpdates chan<- streamUpdate
	currentDone    chan struct{}
}

func newCopilotClient() *copilotClient {
	return &copilotClient{}
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

// resetSession disconnects the current session so the next ask() creates a fresh one.
func (c *copilotClient) resetSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		c.session.Disconnect()
		c.session = nil
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
		Model:     modelName,
		Streaming: true,
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "replace",
			Content: systemPrompt(shell),
		},
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
	})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	c.session = session
	c.sessionModel = modelName
	c.sessionShell = shell

	// Attach event handler once for this session's lifetime.
	// It routes events to whatever stream channel is currently active.
	session.On(func(event copilot.SessionEvent) {
		c.streamMu.Lock()
		updates := c.currentUpdates
		done := c.currentDone
		c.streamMu.Unlock()

		switch d := event.Data.(type) {
		case *copilot.AssistantMessageDeltaData:
			if updates != nil {
				updates <- streamUpdate{delta: d.DeltaContent}
			}
		case *copilot.SessionIdleData:
			// Clear both channels to prevent stale sends
			c.streamMu.Lock()
			c.currentDone = nil
			c.currentUpdates = nil
			c.streamMu.Unlock()
			if done != nil {
				close(done)
			}
		}
	})

	return nil
}

func (c *copilotClient) ask(ctx context.Context, prompt, modelName, shell string, updates chan<- streamUpdate) {
	defer close(updates)

	if err := c.start(ctx); err != nil {
		updates <- streamUpdate{err: err}
		return
	}

	c.mu.Lock()
	if err := c.ensureSession(ctx, modelName, shell); err != nil {
		c.mu.Unlock()
		updates <- streamUpdate{err: err}
		return
	}

	done := make(chan struct{})

	c.streamMu.Lock()
	c.currentUpdates = updates
	c.currentDone = done
	c.streamMu.Unlock()

	session := c.session
	c.mu.Unlock()

	_, err := session.Send(ctx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		updates <- streamUpdate{err: fmt.Errorf("failed to send message: %w", err)}
		return
	}

	<-done
	updates <- streamUpdate{done: true}
}
