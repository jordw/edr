package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"taskqueue/internal"
)

// Config holds the server configuration parsed from environment variables.
type Config struct {
	Port         int           `json:"port"`
	WorkerCount  int           `json:"worker_count"`
	QueueSize    int           `json:"queue_size"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	RedisURL     string        `json:"redis_url"`
	LogLevel     string        `json:"log_level"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Port:         8080,
		WorkerCount:  4,
		QueueSize:    1000,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		RedisURL:     "localhost:6379",
		LogLevel:     "info",
	}
}

// Validate checks that the configuration values are within acceptable ranges.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Port)
	}
	if c.WorkerCount < 1 || c.WorkerCount > 64 {
		return fmt.Errorf("invalid worker count: %d", c.WorkerCount)
	}
	if c.QueueSize < 1 || c.QueueSize > 100000 {
		return fmt.Errorf("invalid queue size: %d", c.QueueSize)
	}
	return nil
}

// Server is the main task queue HTTP server.
type Server struct {
	config  Config
	queue   *internal.TaskQueue
	workers []*internal.Worker
	mux     *http.ServeMux
	mu      sync.RWMutex
	started bool
}

// NewServer creates a server with the given config.
func NewServer(cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	q := internal.NewTaskQueue(cfg.QueueSize)
	workers := make([]*internal.Worker, cfg.WorkerCount)
	for i := range workers {
		workers[i] = internal.NewWorker(i, q)
	}

	s := &Server{
		config:  cfg,
		queue:   q,
		workers: workers,
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s, nil
}

// Start begins serving HTTP and starts all workers.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("server already started")
	}
	s.started = true
	s.mu.Unlock()

	for _, w := range s.workers {
		go w.Run(ctx)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      s.mux,
		ReadTimeout:  s.config.ReadTimeout,
		WriteTimeout: s.config.WriteTimeout,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("server starting on port %d with %d workers", s.config.Port, s.config.WorkerCount)
	return srv.ListenAndServe()
}

// registerRoutes sets up the HTTP handlers.
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/api/tasks", s.handleTasks)
	s.mux.HandleFunc("/api/tasks/", s.handleTaskByID)
	s.mux.HandleFunc("/api/stats", s.handleStats)
	s.mux.HandleFunc("/health", s.handleHealth)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTasks(w, r)
	case http.MethodPost:
		s.createTask(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Extract task ID from URL path
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.queue.Stats()
	fmt.Fprintf(w, `{"pending":%d,"completed":%d,"failed":%d}`,
		stats.Pending, stats.Completed, stats.Failed)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()
	if started {
		w.Write([]byte(`{"status":"ok"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("[]"))
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
}

func main() {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	srv, err := NewServer(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	if err := srv.Start(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
