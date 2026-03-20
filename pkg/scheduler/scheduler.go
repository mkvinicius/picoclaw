// Package scheduler provides task scheduling for PicoClaw.
// Supports natural language schedules ("todo dia às 8h") and cron expressions.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// TaskStatus represents the status of a scheduled task.
type TaskStatus string

const (
	TaskActive    TaskStatus = "active"
	TaskPaused    TaskStatus = "paused"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// Task represents a scheduled automation task.
type Task struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Command     string     `json:"command"`     // natural language command to execute
	Schedule    string     `json:"schedule"`    // natural language or cron
	CronExpr    string     `json:"cron_expr"`   // parsed cron expression
	Status      TaskStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	NextRun     time.Time  `json:"next_run"`
	LastRun     time.Time  `json:"last_run,omitempty"`
	LastResult  string     `json:"last_result,omitempty"`
	RunCount    int        `json:"run_count"`
	MaxRuns     int        `json:"max_runs"` // 0 = unlimited
}

// TaskExecutor is a function that executes a task command.
type TaskExecutor func(ctx context.Context, task Task) (string, error)

// ─────────────────────────────────────────────────────────────────────────────
// Scheduler
// ─────────────────────────────────────────────────────────────────────────────

// Scheduler manages scheduled tasks.
type Scheduler struct {
	tasks    map[string]*Task
	executor TaskExecutor
	dataDir  string
	mu       sync.RWMutex
	stopCh   chan struct{}
}

// NewScheduler creates a new scheduler.
func NewScheduler(dataDir string, executor TaskExecutor) *Scheduler {
	return &Scheduler{
		tasks:    make(map[string]*Task),
		executor: executor,
		dataDir:  dataDir,
		stopCh:   make(chan struct{}),
	}
}

// Start starts the scheduler loop.
func (s *Scheduler) Start(ctx context.Context) error {
	// Load persisted tasks
	if err := s.loadTasks(); err != nil {
		fmt.Printf("Scheduler: could not load tasks: %v\n", err)
	}

	go s.loop(ctx)
	fmt.Printf("📅 Agendador iniciado (%d tarefas carregadas)\n", len(s.tasks))
	return nil
}

// AddTask adds a new task from natural language.
func (s *Scheduler) AddTask(name, naturalSchedule, command string) (*Task, error) {
	cronExpr, err := ParseNaturalSchedule(naturalSchedule)
	if err != nil {
		return nil, fmt.Errorf("não entendi o agendamento '%s': %w", naturalSchedule, err)
	}

	nextRun, err := nextRunTime(cronExpr)
	if err != nil {
		return nil, err
	}

	task := &Task{
		ID:          fmt.Sprintf("task_%d", time.Now().UnixNano()),
		Name:        name,
		Description: fmt.Sprintf("Agendado: %s", naturalSchedule),
		Command:     command,
		Schedule:    naturalSchedule,
		CronExpr:    cronExpr,
		Status:      TaskActive,
		CreatedAt:   time.Now(),
		NextRun:     nextRun,
	}

	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()

	s.saveTasks()
	fmt.Printf("📅 Tarefa agendada: '%s' → %s (próxima: %s)\n", name, naturalSchedule, nextRun.Format("02/01 15:04"))
	return task, nil
}

// PauseTask pauses a task.
func (s *Scheduler) PauseTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("tarefa não encontrada: %s", id)
	}
	task.Status = TaskPaused
	s.saveTasks()
	return nil
}

// ResumeTask resumes a paused task.
func (s *Scheduler) ResumeTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("tarefa não encontrada: %s", id)
	}
	task.Status = TaskActive
	nextRun, _ := nextRunTime(task.CronExpr)
	task.NextRun = nextRun
	s.saveTasks()
	return nil
}

// DeleteTask removes a task.
func (s *Scheduler) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	s.saveTasks()
	return nil
}

// ListTasks returns all tasks.
func (s *Scheduler) ListTasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, *t)
	}
	return tasks
}

// ─────────────────────────────────────────────────────────────────────────────
// Scheduler loop
// ─────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndRun(ctx)
		}
	}
}

func (s *Scheduler) checkAndRun(ctx context.Context) {
	now := time.Now()

	s.mu.Lock()
	var toRun []*Task
	for _, task := range s.tasks {
		if task.Status == TaskActive && !task.NextRun.IsZero() && now.After(task.NextRun) {
			toRun = append(toRun, task)
		}
	}
	s.mu.Unlock()

	for _, task := range toRun {
		go s.executeTask(ctx, task)
	}
}

func (s *Scheduler) executeTask(ctx context.Context, task *Task) {
	fmt.Printf("📅 Executando tarefa agendada: '%s'\n", task.Name)

	result, err := s.executor(ctx, *task)

	s.mu.Lock()
	task.LastRun = time.Now()
	task.RunCount++
	if err != nil {
		task.LastResult = fmt.Sprintf("❌ Erro: %v", err)
		task.Status = TaskFailed
	} else {
		task.LastResult = result
		// Schedule next run
		if task.MaxRuns == 0 || task.RunCount < task.MaxRuns {
			nextRun, err := nextRunTime(task.CronExpr)
			if err == nil {
				task.NextRun = nextRun
				task.Status = TaskActive
			}
		} else {
			task.Status = TaskCompleted
		}
	}
	s.mu.Unlock()
	s.saveTasks()
}

// ─────────────────────────────────────────────────────────────────────────────
// Natural Language Schedule Parser
// ─────────────────────────────────────────────────────────────────────────────

// ParseNaturalSchedule converts natural language to a cron expression.
// Examples:
//   "todo dia às 8h"          → "0 8 * * *"
//   "toda segunda às 9h30"    → "30 9 * * 1"
//   "a cada 30 minutos"       → "*/30 * * * *"
//   "toda hora"               → "0 * * * *"
//   "todo dia útil às 18h"    → "0 18 * * 1-5"
func ParseNaturalSchedule(input string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	s = strings.ReplaceAll(s, "às", "")
	s = strings.ReplaceAll(s, "as", "")
	s = strings.ReplaceAll(s, "h", ":")
	s = strings.TrimSpace(s)

	// "a cada X minutos"
	if strings.Contains(s, "cada") && strings.Contains(s, "minuto") {
		var n int
		fmt.Sscanf(s, "a cada %d minuto", &n)
		if n <= 0 { n = 1 }
		return fmt.Sprintf("*/%d * * * *", n), nil
	}

	// "a cada X horas"
	if strings.Contains(s, "cada") && strings.Contains(s, "hora") {
		var n int
		fmt.Sscanf(s, "a cada %d hora", &n)
		if n <= 0 { n = 1 }
		return fmt.Sprintf("0 */%d * * *", n), nil
	}

	// "toda hora"
	if s == "toda hora" || s == "a cada hora" {
		return "0 * * * *", nil
	}

	// Parse time component (HH:MM or HH)
	hour, minute := 9, 0
	parts := strings.Fields(s)
	for _, p := range parts {
		if strings.Contains(p, ":") {
			fmt.Sscanf(p, "%d:%d", &hour, &minute)
		} else if strings.HasSuffix(p, "h") {
			fmt.Sscanf(p, "%dh", &hour)
		}
	}

	// Day of week
	dayMap := map[string]string{
		"segunda": "1", "terça": "2", "terca": "2",
		"quarta": "3", "quinta": "4", "sexta": "5",
		"sabado": "6", "sábado": "6", "domingo": "0",
	}

	for dayName, dayNum := range dayMap {
		if strings.Contains(s, dayName) {
			return fmt.Sprintf("%d %d * * %s", minute, hour, dayNum), nil
		}
	}

	// "todo dia útil" or "dias úteis"
	if strings.Contains(s, "útil") || strings.Contains(s, "util") || strings.Contains(s, "semana") {
		return fmt.Sprintf("%d %d * * 1-5", minute, hour), nil
	}

	// "todo dia" or "diariamente"
	if strings.Contains(s, "dia") || strings.Contains(s, "diari") || strings.Contains(s, "todo") {
		return fmt.Sprintf("%d %d * * *", minute, hour), nil
	}

	// "toda semana" (Monday)
	if strings.Contains(s, "semana") {
		return fmt.Sprintf("%d %d * * 1", minute, hour), nil
	}

	// "todo mês" (1st of month)
	if strings.Contains(s, "mês") || strings.Contains(s, "mes") {
		return fmt.Sprintf("%d %d 1 * *", minute, hour), nil
	}

	// Default: daily at parsed time
	return fmt.Sprintf("%d %d * * *", minute, hour), nil
}

// nextRunTime calculates the next run time for a cron expression.
// This is a simplified cron parser for the most common patterns.
func nextRunTime(cronExpr string) (time.Time, error) {
	parts := strings.Fields(cronExpr)
	if len(parts) != 5 {
		return time.Time{}, fmt.Errorf("invalid cron expression: %s", cronExpr)
	}

	now := time.Now()
	next := now.Add(time.Minute)

	// Try up to 1 year ahead
	for i := 0; i < 525600; i++ {
		if matchesCron(next, parts) {
			return next.Truncate(time.Minute), nil
		}
		next = next.Add(time.Minute)
	}

	return time.Time{}, fmt.Errorf("no next run time found for: %s", cronExpr)
}

func matchesCron(t time.Time, parts []string) bool {
	return matchField(parts[0], t.Minute(), 0, 59) &&
		matchField(parts[1], t.Hour(), 0, 23) &&
		matchField(parts[2], t.Day(), 1, 31) &&
		matchField(parts[3], int(t.Month()), 1, 12) &&
		matchField(parts[4], int(t.Weekday()), 0, 6)
}

func matchField(expr string, value, min, max int) bool {
	if expr == "*" {
		return true
	}
	// */n
	if strings.HasPrefix(expr, "*/") {
		var n int
		fmt.Sscanf(expr, "*/%d", &n)
		return value%n == 0
	}
	// n-m
	if strings.Contains(expr, "-") {
		var lo, hi int
		fmt.Sscanf(expr, "%d-%d", &lo, &hi)
		return value >= lo && value <= hi
	}
	// exact
	var n int
	fmt.Sscanf(expr, "%d", &n)
	return value == n
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────────────────

func (s *Scheduler) saveTasks() {
	if s.dataDir == "" {
		return
	}
	os.MkdirAll(s.dataDir, 0700)
	path := filepath.Join(s.dataDir, "scheduled_tasks.json")

	s.mu.RLock()
	data, err := json.MarshalIndent(s.tasks, "", "  ")
	s.mu.RUnlock()

	if err == nil {
		os.WriteFile(path, data, 0600)
	}
}

func (s *Scheduler) loadTasks() error {
	if s.dataDir == "" {
		return nil
	}
	path := filepath.Join(s.dataDir, "scheduled_tasks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var tasks map[string]*Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return err
	}

	s.mu.Lock()
	s.tasks = tasks
	// Recalculate next run times for active tasks
	for _, task := range s.tasks {
		if task.Status == TaskActive {
			if nextRun, err := nextRunTime(task.CronExpr); err == nil {
				task.NextRun = nextRun
			}
		}
	}
	s.mu.Unlock()
	return nil
}
