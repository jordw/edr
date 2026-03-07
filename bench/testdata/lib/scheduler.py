"""Task scheduler with dependency resolution, retry policies, and cron-like scheduling."""

from __future__ import annotations

import heapq
import time
import threading
import logging
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Callable, Optional
from collections import defaultdict
from datetime import datetime, timedelta


class ScheduleType(Enum):
    ONCE = "once"
    RECURRING = "recurring"
    CRON = "cron"
    DEPENDENT = "dependent"


class TaskState(Enum):
    PENDING = "pending"
    READY = "ready"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"
    WAITING = "waiting"


@dataclass
class ScheduledTask:
    task_id: str
    task_type: str
    payload: dict[str, Any]
    priority: int = 0
    schedule_type: ScheduleType = ScheduleType.ONCE
    interval_seconds: float = 0
    max_retries: int = 3
    retry_count: int = 0
    state: TaskState = TaskState.PENDING
    created_at: float = field(default_factory=time.time)
    scheduled_at: float = field(default_factory=time.time)
    dependencies: list[str] = field(default_factory=list)
    handler: Optional[Callable] = None

    def __lt__(self, other: ScheduledTask) -> bool:
        if self.priority != other.priority:
            return self.priority > other.priority
        return self.scheduled_at < other.scheduled_at

    def __eq__(self, other: object) -> bool:
        if not isinstance(other, ScheduledTask):
            return NotImplemented
        return self.task_id == other.task_id


class DependencyGraph:
    """Tracks task dependencies and determines execution order."""

    def __init__(self) -> None:
        self._forward: dict[str, set[str]] = defaultdict(set)  # task -> deps
        self._reverse: dict[str, set[str]] = defaultdict(set)  # dep -> dependents
        self._completed: set[str] = set()

    def add_dependency(self, task_id: str, depends_on: str) -> None:
        self._forward[task_id].add(depends_on)
        self._reverse[depends_on].add(task_id)

    def get_dependencies(self, task_id: str) -> set[str]:
        return self._forward.get(task_id, set())

    def get_dependents(self, task_id: str) -> set[str]:
        return self._reverse.get(task_id, set())

    def mark_completed(self, task_id: str) -> None:
        self._completed.add(task_id)

    def is_ready(self, task_id: str) -> bool:
        deps = self._forward.get(task_id, set())
        return deps.issubset(self._completed)

    def topological_sort(self) -> list[str]:
        """Return tasks in valid execution order."""
        in_degree: dict[str, int] = defaultdict(int)
        all_tasks: set[str] = set()

        for task, deps in self._forward.items():
            all_tasks.add(task)
            for dep in deps:
                all_tasks.add(dep)
                in_degree[task] += 1

        queue = [t for t in all_tasks if in_degree[t] == 0]
        result: list[str] = []

        while queue:
            task = queue.pop(0)
            result.append(task)
            for dependent in self._reverse.get(task, set()):
                in_degree[dependent] -= 1
                if in_degree[dependent] == 0:
                    queue.append(dependent)

        if len(result) != len(all_tasks):
            raise ValueError("Circular dependency detected")
        return result

    def remove_task(self, task_id: str) -> None:
        """Remove a task and clean up its edges."""
        for dep in self._forward.pop(task_id, set()):
            self._reverse.get(dep, set()).discard(task_id)
        for dependent in self._reverse.pop(task_id, set()):
            self._forward.get(dependent, set()).discard(task_id)
        self._completed.discard(task_id)



@dataclass
class SchedulerStats:
    tasks_submitted: int = 0
    tasks_completed: int = 0
    tasks_failed: int = 0
    tasks_retried: int = 0
    total_wait_time: float = 0.0
    total_run_time: float = 0.0

    def success_rate(self) -> float:
        total = self.tasks_completed + self.tasks_failed
        if total == 0:
            return 0.0
        return self.tasks_completed / total

    def to_dict(self) -> dict[str, Any]:
        return {
            "tasks_submitted": self.tasks_submitted,
            "tasks_completed": self.tasks_completed,
            "tasks_failed": self.tasks_failed,
            "tasks_retried": self.tasks_retried,
            "total_wait_time": round(self.total_wait_time, 3),
            "total_run_time": round(self.total_run_time, 3),
            "success_rate": round(self.success_rate(), 4),
        }


class Scheduler:
    """Priority-based task scheduler with dependency resolution."""

    def __init__(self, max_workers: int = 4) -> None:
        self._queue: list[ScheduledTask] = []
        self._tasks: dict[str, ScheduledTask] = {}
        self._handlers: dict[str, Callable] = {}
        self._deps = DependencyGraph()
        self._stats = SchedulerStats()
        self._lock = threading.Lock()
        self._running = False
        self._max_workers = max_workers
        self._active_workers = 0
        self._logger = logging.getLogger("scheduler")
        self._condition = threading.Condition(self._lock)

    def schedule(
        self,
        task_id: str,
        task_type: str,
        payload: dict[str, Any] | None = None,
        priority: int = 0,
        delay: float = 0,
        dependencies: list[str] | None = None,
        schedule_type: ScheduleType = ScheduleType.ONCE,
        interval: float = 0,
    ) -> ScheduledTask:
        """Schedule a task for execution."""
        task = ScheduledTask(
            task_id=task_id,
            task_type=task_type,
            payload=payload or {},
            priority=priority,
            schedule_type=schedule_type,
            interval_seconds=interval,
            scheduled_at=time.time() + delay,
            dependencies=dependencies or [],
        )

        with self._lock:
            if task_id in self._tasks:
                raise ValueError(f"Task {task_id} already exists")

            self._tasks[task_id] = task
            self._stats.tasks_submitted += 1

            for dep in task.dependencies:
                self._deps.add_dependency(task_id, dep)

            if self._deps.is_ready(task_id):
                task.state = TaskState.READY
                heapq.heappush(self._queue, task)
            else:
                task.state = TaskState.WAITING

            self._condition.notify()

        self._logger.info("Scheduled task %s (type=%s, priority=%d)", task_id, task_type, priority)
        return task

    def register_handler(self, task_type: str, handler: Callable) -> None:
        """Register a handler function for a task type."""
        self._handlers[task_type] = handler

    def cancel(self, task_id: str) -> bool:
        """Cancel a pending or waiting task."""
        with self._lock:
            task = self._tasks.get(task_id)
            if task is None:
                return False
            if task.state in (TaskState.RUNNING, TaskState.COMPLETED):
                return False
            task.state = TaskState.CANCELLED
            self._deps.remove_task(task_id)
            return True

    def _execute_task(self, task: ScheduledTask) -> None:
        """Execute a single task with retry logic."""
        handler = task.handler or self._handlers.get(task.task_type)
        if handler is None:
            self._logger.error("No handler for task type: %s", task.task_type)
            task.state = TaskState.FAILED
            self._stats.tasks_failed += 1
            return

        start_time = time.time()
        task.state = TaskState.RUNNING

        try:
            handler(task.task_id, task.payload)
            task.state = TaskState.COMPLETED
            self._stats.tasks_completed += 1
            elapsed = time.time() - start_time
            self._stats.total_run_time += elapsed
            self._logger.info("Task %s completed in %.3fs", task.task_id, elapsed)

            with self._lock:
                self._deps.mark_completed(task.task_id)
                self._activate_dependents(task.task_id)

                if task.schedule_type == ScheduleType.RECURRING:
                    self._reschedule(task)

        except Exception as exc:
            task.retry_count += 1
            if task.retry_count <= task.max_retries:
                self._stats.tasks_retried += 1
                backoff = min(2 ** task.retry_count, 60)
                task.scheduled_at = time.time() + backoff
                task.state = TaskState.READY
                with self._lock:
                    heapq.heappush(self._queue, task)
                self._logger.warning(
                    "Task %s failed (attempt %d/%d), retrying in %ds: %s",
                    task.task_id, task.retry_count, task.max_retries, backoff, exc,
                )
            else:
                task.state = TaskState.FAILED
                self._stats.tasks_failed += 1
                self._logger.error("Task %s failed permanently: %s", task.task_id, exc)

    def _activate_dependents(self, completed_task_id: str) -> None:
        """Move dependent tasks to ready state if all deps are met."""
        for dep_id in self._deps.get_dependents(completed_task_id):
            task = self._tasks.get(dep_id)
            if task and task.state == TaskState.WAITING and self._deps.is_ready(dep_id):
                task.state = TaskState.READY
                heapq.heappush(self._queue, task)

    def _reschedule(self, task: ScheduledTask) -> None:
        """Reschedule a recurring task."""
        task.scheduled_at = time.time() + task.interval_seconds
        task.state = TaskState.READY
        task.retry_count = 0
        heapq.heappush(self._queue, task)

    def get_stats(self) -> dict[str, Any]:
        return self._stats.to_dict()

    def get_task(self, task_id: str) -> ScheduledTask | None:
        return self._tasks.get(task_id)

    def pending_count(self) -> int:
        with self._lock:
            return sum(
                1 for t in self._tasks.values()
                if t.state in (TaskState.PENDING, TaskState.READY, TaskState.WAITING)
            )

    def start(self) -> None:
        """Start the scheduler loop."""
        self._running = True
        for i in range(self._max_workers):
            t = threading.Thread(target=self._worker_loop, name=f"scheduler-worker-{i}", daemon=True)
            t.start()

    def stop(self) -> None:
        """Stop the scheduler gracefully."""
        with self._lock:
            self._running = False
            self._condition.notify_all()

    def _worker_loop(self) -> None:
        """Main worker loop — pulls tasks from the priority queue."""
        while self._running:
            task = None
            with self._condition:
                while self._running and not self._queue:
                    self._condition.wait(timeout=1.0)
                if not self._running:
                    break
                now = time.time()
                if self._queue and self._queue[0].scheduled_at <= now:
                    task = heapq.heappop(self._queue)
                    self._active_workers += 1

            if task:
                try:
                    wait_time = time.time() - task.created_at
                    self._stats.total_wait_time += wait_time
                    self._execute_task(task)
                finally:
                    with self._lock:
                        self._active_workers -= 1
