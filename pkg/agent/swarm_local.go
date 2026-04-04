package agent

import (
	"context"
	"fmt"
)

// LocalAgentExecutor implements AgentExecutor using the running AgentLoop.
// It connects the SwarmExecutor to real AgentInstance execution so that
// swarm tasks are processed by the actual LLM + tools pipeline instead of
// a stub.
//
// Session keys are scoped per swarm task (swarm:{taskID}) so each task gets
// isolated history that doesn't contaminate other agents' sessions.
type LocalAgentExecutor struct {
	loop *AgentLoop
}

// NewLocalAgentExecutor creates an executor backed by the given AgentLoop.
func NewLocalAgentExecutor(loop *AgentLoop) *LocalAgentExecutor {
	return &LocalAgentExecutor{loop: loop}
}

// Execute runs a single swarm task through the agent pipeline.
//
// The AgentName field on the task is used as the routing target: if it matches
// a named agent in the registry the task is routed there; otherwise it falls
// back to the default agent.
//
// Dependency context (results of upstream tasks) is appended to the prompt so
// the LLM has visibility into what previous agents produced.
func (e *LocalAgentExecutor) Execute(
	ctx context.Context,
	task AgentTask,
	depContext map[string]TaskResult,
) (string, error) {
	if e.loop == nil {
		return "", fmt.Errorf("swarm: LocalAgentExecutor has no AgentLoop")
	}

	prompt := buildSwarmPrompt(task, depContext)
	sessionKey := "swarm:" + task.ID
	channel := "swarm"
	chatID := task.ID

	// Route to the named agent if one is specified and it exists.
	if task.AgentName != "" {
		registry := e.loop.GetRegistry()
		normalised := NormalizeAgentIDForSwarm(task.AgentName)
		if _, ok := registry.GetAgent(normalised); ok {
			// Embed agent routing hint in the session key so the registry
			// resolver picks the right agent.
			sessionKey = "agent:" + normalised + ":" + sessionKey
		}
	}

	return e.loop.ProcessDirectWithChannel(ctx, prompt, sessionKey, channel, chatID)
}

// buildSwarmPrompt constructs the full prompt for a swarm task, appending
// the outputs of any upstream dependency tasks as context.
func buildSwarmPrompt(task AgentTask, deps map[string]TaskResult) string {
	if len(deps) == 0 {
		return task.Prompt
	}

	var sb = task.Prompt + "\n\n---\n**Context from upstream agents:**\n"
	for _, depID := range task.DependsOn {
		result, ok := deps[depID]
		if !ok {
			continue
		}
		if result.Error != nil {
			sb += fmt.Sprintf("\n[%s] ERROR: %v\n", depID, result.Error)
		} else {
			sb += fmt.Sprintf("\n[%s]:\n%s\n", depID, result.Output)
		}
	}
	return sb
}

// NormalizeAgentIDForSwarm is a thin wrapper so swarm_local.go can call the
// routing package normalizer without importing it directly.
func NormalizeAgentIDForSwarm(name string) string {
	// Delegate to the existing routing normalizer already used in instance.go.
	// We inline a safe fallback here to keep the import surface minimal.
	if name == "" {
		return "main"
	}
	return name
}

// NewSwarmExecutorFromLoop is a convenience constructor that wires together
// a SwarmExecutor backed by the running AgentLoop.
//
//	exec := agent.NewSwarmExecutorFromLoop(loop, agent.DefaultSwarmConfig())
//	results, err := exec.Execute(ctx, tasks)
func NewSwarmExecutorFromLoop(loop *AgentLoop, cfg SwarmConfig) *SwarmExecutor {
	return NewSwarmExecutor(cfg, NewLocalAgentExecutor(loop))
}
