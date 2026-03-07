package internal

import (
	"fmt"
	"sync"
	"time"
)

// Priority levels for tasks in the queue.
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 1
	PriorityHigh   Priority = 2
	PriorityCritical Priority = 3
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
)

// Task is a unit of work submitted to the queue.
type Task struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Priority  Priority          `json:"priority"`
	Status    TaskStatus        `json:"status"`
	Payload   map[string]any    `json:"payload"`
	Result    any               `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	StartedAt *time.Time        `json:"started_at,omitempty"`
	DoneAt    *time.Time        `json:"done_at,omitempty"`
	Retries   int               `json:"retries"`
	MaxRetry  int               `json:"max_retry"`
	Tags      []string          `json:"tags,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Duration returns how long the task took to execute, or zero if not complete.
func (t *Task) Duration() time.Duration {
	if t.StartedAt == nil || t.DoneAt == nil {
		return 0
	}
	return t.DoneAt.Sub(*t.StartedAt)
}

// IsTerminal returns true if the task is in a final state.
func (t *Task) IsTerminal() bool {
	return t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled
}

// CanRetry checks whether the task has retries remaining.
func (t *Task) CanRetry() bool {
	return t.Retries < t.MaxRetry && t.Status == StatusFailed
}

// QueueStats holds aggregate metrics about the queue.
type QueueStats struct {
	Pending   int           `json:"pending"`
	Running   int           `json:"running"`
	Completed int           `json:"completed"`
	Failed    int           `json:"failed"`
	AvgWait   time.Duration `json:"avg_wait_ms"`
}

// TaskQueue is a thread-safe priority queue for tasks.
type TaskQueue struct {
	mu        sync.Mutex
	cond      *sync.Cond
	tasks     []*Task
	index     map[string]*Task
	maxSize   int
	completed int
	failed    int
	closed    bool
}

// NewTaskQueue creates a queue with the given capacity.
func NewTaskQueue(maxSize int) *TaskQueue {
	q := &TaskQueue{
		tasks:   make([]*Task, 0, maxSize),
		index:   make(map[string]*Task),
		maxSize: maxSize,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Enqueue adds a task to the queue. Returns an error if the queue is full.
func (q *TaskQueue) Enqueue(task *Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("queue is closed")
	}
	if len(q.tasks) >= q.maxSize {
		return fmt.Errorf("queue full (max %d)", q.maxSize)
	}
	if _, exists := q.index[task.ID]; exists {
		return fmt.Errorf("duplicate task ID: %s", task.ID)
	}

	task.Status = StatusPending
	task.CreatedAt = time.Now()
	q.tasks = append(q.tasks, task)
	q.index[task.ID] = task

	// Sort by priority (higher first), then by creation time
	q.sortLocked()
	q.cond.Signal()
	return nil
}

// Dequeue removes and returns the highest-priority pending task.
// Blocks until a task is available or the queue is closed.
func (q *TaskQueue) Dequeue() (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.pendingLocked()) == 0 && !q.closed {
		q.cond.Wait()
	}

	pending := q.pendingLocked()
	if len(pending) == 0 {
		return nil, false
	}

	task := pending[0]
	now := time.Now()
	task.Status = StatusRunning
	task.StartedAt = &now
	return task, true
}

// Complete marks a task as successfully completed.
func (q *TaskQueue) Complete(id string, result any) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	task, ok := q.index[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	now := time.Now()
	task.Status = StatusCompleted
	task.Result = result
	task.DoneAt = &now
	q.completed++
	return nil
}

// Fail marks a task as failed, optionally requeueing if retries remain.
func (q *TaskQueue) Fail(id string, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	task, ok := q.index[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	task.Retries++
	if task.CanRetry() {
		task.Status = StatusPending
		task.Error = errMsg
		q.sortLocked()
		q.cond.Signal()
		return nil
	}

	now := time.Now()
	task.Status = StatusFailed
	task.Error = errMsg
	task.DoneAt = &now
	q.failed++
	return nil
}

// Cancel marks a pending task as cancelled.
func (q *TaskQueue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	task, ok := q.index[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}
	if task.Status != StatusPending {
		return fmt.Errorf("can only cancel pending tasks, got %s", task.Status)
	}

	task.Status = StatusCancelled
	return nil
}

// Get retrieves a task by ID.
func (q *TaskQueue) Get(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task, ok := q.index[id]
	return task, ok
}

// Stats returns aggregate queue metrics.
func (q *TaskQueue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()

	pending := len(q.pendingLocked())
	running := 0
	var totalWait time.Duration
	waitCount := 0

	for _, t := range q.tasks {
		if t.Status == StatusRunning {
			running++
		}
		if t.StartedAt != nil {
			totalWait += t.StartedAt.Sub(t.CreatedAt)
			waitCount++
		}
	}

	var avgWait time.Duration
	if waitCount > 0 {
		avgWait = totalWait / time.Duration(waitCount)
	}

	return QueueStats{
		Pending:   pending,
		Running:   running,
		Completed: q.completed,
		Failed:    q.failed,
		AvgWait:   avgWait,
	}
}

// Close shuts down the queue and wakes any blocked consumers.
func (q *TaskQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// pendingLocked returns pending tasks. Must be called with mu held.
func (q *TaskQueue) pendingLocked() []*Task {
	var result []*Task
	for _, t := range q.tasks {
		if t.Status == StatusPending {
			result = append(result, t)
		}
	}
	return result
}

// sortLocked sorts tasks by priority descending, then by creation time.
func (q *TaskQueue) sortLocked() {
	tasks := q.tasks
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			if tasks[j].Priority > tasks[j-1].Priority {
				tasks[j], tasks[j-1] = tasks[j-1], tasks[j]
			} else if tasks[j].Priority == tasks[j-1].Priority &&
				tasks[j].CreatedAt.Before(tasks[j-1].CreatedAt) {
				tasks[j], tasks[j-1] = tasks[j-1], tasks[j]
			} else {
				break
			}
		}
	}
}
