package internal

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"
)

// HandlerFunc processes a task and returns a result or error.
type HandlerFunc func(ctx context.Context, task *Task) (any, error)

// HandlerRegistry maps task types to their handler functions.
type HandlerRegistry struct {
	handlers map[string]HandlerFunc
}

// NewHandlerRegistry creates an empty registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[string]HandlerFunc),
	}
}

// Register adds a handler for a task type.
func (r *HandlerRegistry) Register(taskType string, fn HandlerFunc) {
	r.handlers[taskType] = fn
}

// Get retrieves the handler for a task type.
func (r *HandlerRegistry) Get(taskType string) (HandlerFunc, bool) {
	fn, ok := r.handlers[taskType]
	return fn, ok
}

// Worker pulls tasks from a queue and processes them.
type Worker struct {
	ID       int
	queue    *TaskQueue
	registry *HandlerRegistry
	stats    WorkerStats
}

// WorkerStats tracks per-worker performance metrics.
type WorkerStats struct {
	TasksProcessed int           `json:"tasks_processed"`
	TasksFailed    int           `json:"tasks_failed"`
	TotalDuration  time.Duration `json:"total_duration_ms"`
	LastTaskAt     *time.Time    `json:"last_task_at,omitempty"`
}

// NewWorker creates a worker attached to a queue.
func NewWorker(id int, queue *TaskQueue) *Worker {
	return &Worker{
		ID:       id,
		queue:    queue,
		registry: NewHandlerRegistry(),
	}
}

// Run starts the worker loop. It blocks until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	log.Printf("worker %d started", w.ID)
	defer log.Printf("worker %d stopped", w.ID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		task, ok := w.queue.Dequeue()
		if !ok {
			return // queue closed
		}

		w.processTask(ctx, task)
	}
}

// processTask executes a single task with timeout and panic recovery.
func (w *Worker) processTask(ctx context.Context, task *Task) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		w.stats.TotalDuration += elapsed
		now := time.Now()
		w.stats.LastTaskAt = &now
	}()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("worker %d: panic in task %s: %v", w.ID, task.ID, r)
			w.queue.Fail(task.ID, fmt.Sprintf("panic: %v", r))
			w.stats.TasksFailed++
		}
	}()

	handler, ok := w.registry.Get(task.Type)
	if !ok {
		handler = w.defaultHandler
	}

	// Execute with a per-task timeout
	taskCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := handler(taskCtx, task)
	if err != nil {
		log.Printf("worker %d: task %s failed: %v", w.ID, task.ID, err)
		w.queue.Fail(task.ID, err.Error())
		w.stats.TasksFailed++
		return
	}

	w.queue.Complete(task.ID, result)
	w.stats.TasksProcessed++
}

// defaultHandler simulates work with a random delay.
func (w *Worker) defaultHandler(ctx context.Context, task *Task) (any, error) {
	delay := time.Duration(50+rand.Intn(200)) * time.Millisecond
	select {
	case <-time.After(delay):
		return map[string]any{
			"worker_id":  w.ID,
			"task_id":    task.ID,
			"processed":  true,
			"duration_ms": delay.Milliseconds(),
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stats returns a copy of the worker's stats.
func (w *Worker) Stats() WorkerStats {
	return w.stats
}

// WorkerPool manages a set of workers.
type WorkerPool struct {
	workers []*Worker
	queue   *TaskQueue

}

// NewWorkerPool creates a pool with n workers sharing a queue.
func NewWorkerPool(n int, queue *TaskQueue) *WorkerPool {
	pool := &WorkerPool{
		workers: make([]*Worker, n),
		queue:   queue,
	}
	for i := range pool.workers {
		pool.workers[i] = NewWorker(i, queue)
	}
	return pool
}

// Start launches all workers in the pool.
func (p *WorkerPool) Start(ctx context.Context) {
	for _, w := range p.workers {
		go w.Run(ctx)
	}
}

// Stop closes the queue, which causes workers to drain and exit.
func (p *WorkerPool) Stop() {
	p.queue.Close()
}

// AggregateStats returns combined stats across all workers.
func (p *WorkerPool) AggregateStats() WorkerStats {
	var agg WorkerStats
	for _, w := range p.workers {
		s := w.Stats()
		agg.TasksProcessed += s.TasksProcessed
		agg.TasksFailed += s.TasksFailed
		agg.TotalDuration += s.TotalDuration
		if s.LastTaskAt != nil && (agg.LastTaskAt == nil || s.LastTaskAt.After(*agg.LastTaskAt)) {
			agg.LastTaskAt = s.LastTaskAt
		}
	}
	return agg
}
