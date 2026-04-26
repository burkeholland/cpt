package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
)

const systemPromptTemplate = `You translate natural language into shell commands. Output rules:
1. Print one command per line — nothing else
2. No prose, no explanations, no markdown, no code fences, no bullet points, no numbering
3. If there are multiple alternative ways, list the best 2-3 as separate lines
4. Each line must be a valid, copy-pasteable shell command
5. Target shell: %s on %s

Example input:  "kill process on port 3000"
Example output (bash/zsh):
lsof -ti:3000 | xargs kill -9
fuser -k 3000/tcp
Example output (PowerShell):
Stop-Process -Id (Get-NetTCPConnection -LocalPort 3000).OwningProcess -Force`

func systemPrompt(shell string) string {
	return fmt.Sprintf(systemPromptTemplate, shell, runtime.GOOS)
}

type copilotClient struct {
	client  *copilot.Client
	models  []string
	mu      sync.Mutex
	started bool
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
	if c.client != nil && c.started {
		c.client.Stop()
		c.started = false
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

func (c *copilotClient) ask(ctx context.Context, prompt, modelName, shell string, updates chan<- streamUpdate) {
	defer close(updates)

	if err := c.start(ctx); err != nil {
		updates <- streamUpdate{err: err}
		return
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
		updates <- streamUpdate{err: fmt.Errorf("failed to create session: %w", err)}
		return
	}
	defer session.Disconnect()

	done := make(chan struct{})

	session.On(func(event copilot.SessionEvent) {
		switch d := event.Data.(type) {
		case *copilot.AssistantMessageDeltaData:
			updates <- streamUpdate{delta: d.DeltaContent}
		case *copilot.SessionIdleData:
			close(done)
		}
	})

	_, err = session.Send(ctx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		updates <- streamUpdate{err: fmt.Errorf("failed to send message: %w", err)}
		return
	}

	<-done
	updates <- streamUpdate{done: true}
}
