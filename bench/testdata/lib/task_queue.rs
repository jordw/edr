//! Task queue with priority scheduling, worker pool, and retry semantics.

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex, Condvar};
use std::thread;
use std::time::{Duration, Instant};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Priority {
    Low,
    Normal,
    High,
    Critical,
}

impl Priority {
    fn as_u8(&self) -> u8 {
        match self {
            Priority::Low => 0,
            Priority::Normal => 1,
            Priority::High => 2,
            Priority::Critical => 3,
        }
    }
}

impl PartialOrd for Priority {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Priority {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        self.as_u8().cmp(&other.as_u8())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TaskStatus {
    Pending,
    Running,
    Completed,
    Failed,
    Cancelled,
}

#[derive(Debug, Clone)]
pub struct Task {
    pub id: u64,
    pub task_type: String,
    pub priority: Priority,
    pub status: TaskStatus,
    pub payload: HashMap<String, String>,
    pub created_at: Instant,
    pub started_at: Option<Instant>,
    pub retries: u32,
    pub max_retries: u32,
    pub error: Option<String>,
}

impl Task {
    pub fn new(id: u64, task_type: &str, priority: Priority, payload: HashMap<String, String>) -> Self {
        Task {
            id,
            task_type: task_type.to_string(),
            priority,
            status: TaskStatus::Pending,
            payload,
            created_at: Instant::now(),
            started_at: None,
            retries: 0,
            max_retries: 3,
            error: None,
        }
    }

    pub fn with_max_retries(mut self, max_retries: u32) -> Self {
        self.max_retries = max_retries;
        self
    }
}

#[derive(Debug, Clone, Default)]
pub struct QueueStats {
    pub pending: usize,
    pub running: usize,
    pub completed: usize,
    pub failed: usize,
    pub cancelled: usize,
}

impl QueueStats {
    pub fn total(&self) -> usize {
        self.pending + self.running + self.completed + self.failed + self.cancelled
    }

    pub fn success_rate(&self) -> f64 {
        let finished = self.completed + self.failed;
        if finished == 0 { return 0.0; }
        self.completed as f64 / finished as f64
    }
}

pub struct TaskQueue {
    queues: Vec<VecDeque<u64>>,        // one deque per priority level
    index: HashMap<u64, Task>,
    capacity: usize,
    next_id: u64,
}

impl TaskQueue {
    pub fn new(capacity: usize) -> Self {
        TaskQueue {
            queues: vec![VecDeque::new(); 4], // Low, Normal, High, Critical
            index: HashMap::new(),
            capacity,
            next_id: 1,
        }
    }

    pub fn enqueue(&mut self, task_type: &str, priority: Priority, payload: HashMap<String, String>) -> Result<u64, String> {
        if self.index.len() >= self.capacity {
            return Err(format!("Queue at capacity ({})", self.capacity));
        }
        let id = self.next_id;
        self.next_id += 1;
        let task = Task::new(id, task_type, priority, payload);
        self.queues[priority.as_u8() as usize].push_back(id);
        self.index.insert(id, task);
        Ok(id)
    }

    pub fn dequeue(&mut self) -> Option<u64> {
        // Drain from highest priority first
        for queue in self.queues.iter_mut().rev() {
            while let Some(id) = queue.pop_front() {
                if let Some(task) = self.index.get_mut(&id) {
                    if task.status == TaskStatus::Pending {
                        task.status = TaskStatus::Running;
                        task.started_at = Some(Instant::now());
                        return Some(id);
                    }
                }
            }
        }
        None
    }

    pub fn complete(&mut self, id: u64) -> Result<(), String> {
        let task = self.index.get_mut(&id).ok_or_else(|| format!("Task {} not found", id))?;
        if task.status != TaskStatus::Running {
            return Err(format!("Task {} is {:?}, not Running", id, task.status));
        }
        task.status = TaskStatus::Completed;
        Ok(())
    }

    pub fn fail(&mut self, id: u64, error: &str) -> Result<bool, String> {
        let task = self.index.get_mut(&id).ok_or_else(|| format!("Task {} not found", id))?;
        if task.status != TaskStatus::Running {
            return Err(format!("Task {} is {:?}, not Running", id, task.status));
        }
        task.retries += 1;
        task.error = Some(error.to_string());
        if task.retries < task.max_retries {
            task.status = TaskStatus::Pending;
            let priority = task.priority;
            self.queues[priority.as_u8() as usize].push_back(id);
            Ok(true) // will retry
        } else {
            task.status = TaskStatus::Failed;
            Ok(false) // permanently failed
        }
    }

    pub fn cancel(&mut self, id: u64) -> Result<(), String> {
        let task = self.index.get_mut(&id).ok_or_else(|| format!("Task {} not found", id))?;
        if task.status == TaskStatus::Completed || task.status == TaskStatus::Failed {
            return Err(format!("Cannot cancel task {} in {:?} state", id, task.status));
        }
        task.status = TaskStatus::Cancelled;
        Ok(())
    }

    pub fn get(&self, id: u64) -> Option<&Task> {
        self.index.get(&id)
    }

    pub fn stats(&self) -> QueueStats {
        let mut s = QueueStats::default();
        for task in self.index.values() {
            match task.status {
                TaskStatus::Pending => s.pending += 1,
                TaskStatus::Running => s.running += 1,
                TaskStatus::Completed => s.completed += 1,
                TaskStatus::Failed => s.failed += 1,
                TaskStatus::Cancelled => s.cancelled += 1,
            }
        }
        s
    }

    pub fn len(&self) -> usize {
        self.index.len()
    }

    pub fn is_empty(&self) -> bool {
        self.index.is_empty()
    }
}

pub struct SharedQueue {
    queue: Mutex<TaskQueue>,
    condvar: Condvar,
}

impl SharedQueue {
    pub fn new(capacity: usize) -> Arc<Self> {
        Arc::new(SharedQueue {
            queue: Mutex::new(TaskQueue::new(capacity)),
            condvar: Condvar::new(),
        })
    }
}

pub struct Worker {
    pub id: usize,
    queue: Arc<SharedQueue>,
    running: Arc<Mutex<bool>>,
}

impl Worker {
    pub fn new(id: usize, queue: Arc<SharedQueue>) -> Self {
        Worker {
            id,
            queue,
            running: Arc::new(Mutex::new(false)),
        }
    }

    pub fn run(&self) {
        *self.running.lock().unwrap() = true;
        while *self.running.lock().unwrap() {
            let task_id = {
                let mut q = self.queue.queue.lock().unwrap();
                match q.dequeue() {
                    Some(id) => id,
                    None => {
                        // Wait for new tasks with a timeout to allow shutdown checks
                        let (lock, _timeout) = self.queue.condvar
                            .wait_timeout(q, Duration::from_millis(100))
                            .unwrap();
                        drop(lock);
                        continue;
                    }
                }
            };
            self.process_task(task_id);
        }
    }

    fn process_task(&self, task_id: u64) {
        let task_type = {
            let q = self.queue.queue.lock().unwrap();
            q.get(task_id).map(|t| t.task_type.clone())
        };
        let Some(task_type) = task_type else { return };

        // Simulate work based on task type
        let result = match task_type.as_str() {
            "fast" => {
                thread::sleep(Duration::from_millis(10));
                Ok(())
            }
            "slow" => {
                thread::sleep(Duration::from_millis(100));
                Ok(())
            }
            "flaky" => {
                // Simulate intermittent failures
                let now = Instant::now();
                if now.elapsed().subsec_nanos() % 3 == 0 {
                    Err("transient failure".to_string())
                } else {
                    Ok(())
                }
            }
            _ => Ok(()),
        };

        let mut q = self.queue.queue.lock().unwrap();
        match result {
            Ok(()) => { let _ = q.complete(task_id); }
            Err(e) => { let _ = q.fail(task_id, &e); }
        }
    }

    pub fn stop(&self) {
        *self.running.lock().unwrap() = false;
    }
}

pub struct WorkerPool {
    workers: Vec<Worker>,
    queue: Arc<SharedQueue>,
    handles: Vec<Option<thread::JoinHandle<()>>>,
}

impl WorkerPool {
    pub fn new(size: usize, queue: Arc<SharedQueue>) -> Self {
        let workers: Vec<Worker> = (0..size)
            .map(|id| Worker::new(id, Arc::clone(&queue)))
            .collect();
        WorkerPool {
            workers,
            queue,
            handles: Vec::new(),
        }
    }

    pub fn start(&mut self) {
        // Workers need to be shared across threads via Arc
        for i in 0..self.workers.len() {
            let queue = Arc::clone(&self.queue);
            let worker_id = self.workers[i].id;
            let running = Arc::clone(&self.workers[i].running);
            let handle = thread::spawn(move || {
                let w = Worker { id: worker_id, queue, running };
                w.run();
            });
            self.handles.push(Some(handle));
        }
    }

    pub fn stop(&mut self) {
        for worker in &self.workers {
            worker.stop();
        }
        self.queue.condvar.notify_all();
        for handle in &mut self.handles {
            if let Some(h) = handle.take() {
                let _ = h.join();
            }
        }
    }

    pub fn aggregate_stats(&self) -> QueueStats {
        let q = self.queue.queue.lock().unwrap();
        q.stats()
    }
}
