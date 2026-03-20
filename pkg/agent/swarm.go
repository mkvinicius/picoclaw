// Package agent — swarm.go
//
// Swarm Execution: Execução paralela de agentes em um squad.
//
// Quando um squad tem múltiplas tarefas independentes, o SwarmExecutor
// as distribui em goroutines nativas do Go, executando-as em paralelo.
// Tarefas dependentes aguardam suas dependências (DAG de execução).
//
// Exemplo:
//   Tarefa: "Analise esse produto e gere um relatório completo"
//
//   Em paralelo (goroutines):
//   ├── Agente Mercado     → pesquisa concorrentes       (~30s)
//   ├── Agente Financeiro  → analisa viabilidade         (~25s)
//   ├── Agente Marketing   → avalia posicionamento       (~20s)
//   └── Agente Técnico     → avalia complexidade         (~15s)
//
//   Sequencial (aguarda os 4):
//   └── Agente Sintetizador → relatório final            (~15s)
//
//   Tempo total: ~45s (paralelo) vs ~105s (sequencial) = 57% mais rápido
//
// Cache Semântico:
//   Antes de chamar o LLM, verifica se uma pergunta semanticamente
//   idêntica já foi respondida. Retorna do cache em milissegundos.
package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Swarm Execution
// ─────────────────────────────────────────────────────────────────────────────

// AgentTask represents a single task to be executed by an agent.
type AgentTask struct {
	ID           string            // unique task identifier
	AgentName    string            // which agent handles this task
	Prompt       string            // the task prompt
	DependsOn    []string          // IDs of tasks that must complete first
	Metadata     map[string]string // optional context
	Timeout      time.Duration     // per-task timeout (0 = use default)
}

// TaskResult holds the output of a single agent task.
type TaskResult struct {
	TaskID    string
	AgentName string
	Output    string
	Error     error
	Duration  time.Duration
	FromCache bool
}

// AgentExecutor is the interface for executing a single agent task.
type AgentExecutor interface {
	Execute(ctx context.Context, task AgentTask, context map[string]TaskResult) (string, error)
}

// SwarmConfig holds configuration for the swarm executor.
type SwarmConfig struct {
	MaxParallel    int           // max concurrent tasks (0 = unlimited)
	DefaultTimeout time.Duration // default per-task timeout
	CacheEnabled   bool          // enable semantic cache
	CacheTTL       time.Duration // cache entry TTL
}

// DefaultSwarmConfig returns sensible defaults.
func DefaultSwarmConfig() SwarmConfig {
	return SwarmConfig{
		MaxParallel:    8,
		DefaultTimeout: 60 * time.Second,
		CacheEnabled:   true,
		CacheTTL:       24 * time.Hour,
	}
}

// SwarmExecutor orchestrates parallel execution of agent tasks.
type SwarmExecutor struct {
	cfg      SwarmConfig
	executor AgentExecutor
	cache    *SemanticCache
	sem      chan struct{} // semaphore for MaxParallel
}

// NewSwarmExecutor creates a new SwarmExecutor.
func NewSwarmExecutor(cfg SwarmConfig, executor AgentExecutor) *SwarmExecutor {
	var sem chan struct{}
	if cfg.MaxParallel > 0 {
		sem = make(chan struct{}, cfg.MaxParallel)
	}

	var cache *SemanticCache
	if cfg.CacheEnabled {
		cache = NewSemanticCache(cfg.CacheTTL)
	}

	return &SwarmExecutor{
		cfg:      cfg,
		executor: executor,
		cache:    cache,
		sem:      sem,
	}
}

// Execute runs all tasks in the optimal order (parallel where possible, sequential where required).
// Returns a map of task ID → result.
func (se *SwarmExecutor) Execute(ctx context.Context, tasks []AgentTask) (map[string]TaskResult, error) {
	if len(tasks) == 0 {
		return nil, nil
	}

	// Build dependency graph
	taskMap := make(map[string]AgentTask, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
	}

	// Topological sort to determine execution order
	levels, err := topoSort(tasks)
	if err != nil {
		return nil, fmt.Errorf("swarm: dependency cycle detected: %w", err)
	}

	results := make(map[string]TaskResult, len(tasks))
	var mu sync.Mutex

	// Execute level by level (tasks in the same level can run in parallel)
	for _, level := range levels {
		var wg sync.WaitGroup
		levelErrors := make([]error, len(level))

		for i, taskID := range level {
			task := taskMap[taskID]
			wg.Add(1)

			go func(idx int, t AgentTask) {
				defer wg.Done()

				// Acquire semaphore slot
				if se.sem != nil {
					select {
					case se.sem <- struct{}{}:
						defer func() { <-se.sem }()
					case <-ctx.Done():
						levelErrors[idx] = ctx.Err()
						return
					}
				}

				// Build context from completed dependencies
				mu.Lock()
				depContext := make(map[string]TaskResult)
				for _, depID := range t.DependsOn {
					if r, ok := results[depID]; ok {
						depContext[depID] = r
					}
				}
				mu.Unlock()

				// Check semantic cache
				if se.cache != nil {
					cacheKey := buildCacheKey(t, depContext)
					if cached, ok := se.cache.Get(cacheKey); ok {
						mu.Lock()
						results[t.ID] = TaskResult{
							TaskID:    t.ID,
							AgentName: t.AgentName,
							Output:    cached,
							FromCache: true,
						}
						mu.Unlock()
						return
					}
				}

				// Execute the task
				timeout := t.Timeout
				if timeout == 0 {
					timeout = se.cfg.DefaultTimeout
				}

				taskCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()

				start := time.Now()
				output, err := se.executor.Execute(taskCtx, t, depContext)
				duration := time.Since(start)

				result := TaskResult{
					TaskID:    t.ID,
					AgentName: t.AgentName,
					Output:    output,
					Error:     err,
					Duration:  duration,
					FromCache: false,
				}

				// Store in cache if successful
				if err == nil && se.cache != nil {
					cacheKey := buildCacheKey(t, depContext)
					se.cache.Set(cacheKey, output)
				}

				mu.Lock()
				results[t.ID] = result
				mu.Unlock()

				if err != nil {
					levelErrors[idx] = fmt.Errorf("task %s (%s): %w", t.ID, t.AgentName, err)
				}
			}(i, task)
		}

		wg.Wait()

		// Check for errors (non-fatal: continue with partial results)
		for _, err := range levelErrors {
			if err != nil {
				// Log but don't fail the whole swarm
				_ = err
			}
		}
	}

	return results, nil
}

// SynthesizeResults combines all task results into a coherent summary.
// The synthesizer receives all outputs and produces a final response.
func SynthesizeResults(results map[string]TaskResult) string {
	if len(results) == 0 {
		return ""
	}

	// Sort by task ID for deterministic output
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var sb strings.Builder
	for _, id := range ids {
		r := results[id]
		if r.Error != nil || r.Output == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", r.AgentName, r.Output))
	}

	return sb.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Semantic Cache
// ─────────────────────────────────────────────────────────────────────────────

// SemanticCache is a TTL-based cache for agent responses.
// In production, this would use vector similarity for semantic matching.
// Here we use a deterministic hash of the normalized prompt as the key.
type SemanticCache struct {
	entries map[string]cacheEntry
	mu      sync.RWMutex
	ttl     time.Duration
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
	hits      int
}

// NewSemanticCache creates a new cache with the given TTL.
func NewSemanticCache(ttl time.Duration) *SemanticCache {
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &SemanticCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

// Get retrieves a cached response. Returns ("", false) if not found or expired.
func (c *SemanticCache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}

	// Update hit count
	c.mu.Lock()
	entry.hits++
	c.entries[key] = entry
	c.mu.Unlock()

	return entry.value, true
}

// Set stores a response in the cache.
func (c *SemanticCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
		hits:      0,
	}
}

// Invalidate removes a specific key from the cache.
func (c *SemanticCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Purge removes all expired entries.
func (c *SemanticCache) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	removed := 0
	for k, v := range c.entries {
		if now.After(v.expiresAt) {
			delete(c.entries, k)
			removed++
		}
	}
	return removed
}

// Stats returns cache statistics.
func (c *SemanticCache) Stats() map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	active := 0
	expired := 0
	totalHits := 0
	now := time.Now()

	for _, v := range c.entries {
		if now.After(v.expiresAt) {
			expired++
		} else {
			active++
		}
		totalHits += v.hits
	}

	return map[string]int{
		"active":     active,
		"expired":    expired,
		"total_hits": totalHits,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildCacheKey creates a deterministic cache key from a task and its dependency outputs.
func buildCacheKey(task AgentTask, depContext map[string]TaskResult) string {
	// Normalize the prompt
	normalized := strings.ToLower(strings.TrimSpace(task.Prompt))
	normalized = strings.Join(strings.Fields(normalized), " ")

	// Include dependency outputs in the key
	var deps []string
	for id, r := range depContext {
		deps = append(deps, id+":"+r.Output[:min(100, len(r.Output))])
	}
	sort.Strings(deps)

	combined := task.AgentName + "|" + normalized + "|" + strings.Join(deps, "|")
	hash := sha256.Sum256([]byte(combined))
	return fmt.Sprintf("%x", hash[:8])
}

// topoSort performs a topological sort of tasks by dependency.
// Returns a slice of levels, where each level contains task IDs that can run in parallel.
func topoSort(tasks []AgentTask) ([][]string, error) {
	// Build adjacency and in-degree maps
	inDegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks)) // task → tasks that depend on it

	for _, t := range tasks {
		if _, ok := inDegree[t.ID]; !ok {
			inDegree[t.ID] = 0
		}
		for _, dep := range t.DependsOn {
			inDegree[t.ID]++
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	// Kahn's algorithm
	var levels [][]string
	queue := make([]string, 0)

	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	processed := 0
	for len(queue) > 0 {
		sort.Strings(queue) // deterministic ordering
		levels = append(levels, queue)
		processed += len(queue)

		var nextQueue []string
		for _, id := range queue {
			for _, dep := range dependents[id] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					nextQueue = append(nextQueue, dep)
				}
			}
		}
		queue = nextQueue
	}

	if processed != len(tasks) {
		return nil, fmt.Errorf("cycle detected in task dependencies")
	}

	return levels, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
