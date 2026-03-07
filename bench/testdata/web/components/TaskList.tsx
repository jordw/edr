import React, { useState, useEffect, useCallback, useMemo } from 'react';

// Mirrors the Go Task struct for type safety
interface Task {
  id: string;
  type: string;
  priority: Priority;
  status: TaskStatus;
  payload: Record<string, unknown>;
  result?: unknown;
  error?: string;
  created_at: string;
  started_at?: string;
  done_at?: string;
  retries: number;
  max_retry: number;
  tags?: string[];
}

type Priority = 0 | 1 | 2 | 3;
type TaskStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';

interface TaskFilter {
  status?: TaskStatus;
  priority?: Priority;
  type?: string;
  search?: string;
}

interface TaskListProps {
  endpoint: string;
  pollInterval?: number;
  onTaskSelect?: (task: Task) => void;
  initialFilter?: TaskFilter;
}

const PRIORITY_LABELS: Record<Priority, string> = {
  0: 'Low',
  1: 'Normal',
  2: 'High',
  3: 'Critical',
};

const STATUS_COLORS: Record<TaskStatus, string> = {
  pending: '#f59e0b',
  running: '#3b82f6',
  completed: '#10b981',
  failed: '#ef4444',
  cancelled: '#6b7280',
};

function formatDuration(start: string, end?: string): string {
  const startTime = new Date(start).getTime();
  const endTime = end ? new Date(end).getTime() : Date.now();
  const ms = endTime - startTime;

  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`;
}

function filterTasks(tasks: Task[], filter: TaskFilter): Task[] {
  return tasks.filter(task => {
    if (filter.status && task.status !== filter.status) return false;
    if (filter.priority !== undefined && task.priority !== filter.priority) return false;
    if (filter.type && task.type !== filter.type) return false;
    if (filter.search) {
      const q = filter.search.toLowerCase();
      return task.id.toLowerCase().includes(q) ||
        task.type.toLowerCase().includes(q) ||
        (task.tags?.some(t => t.toLowerCase().includes(q)) ?? false);
    }
    return true;
  });
}

async function fetchTasks(endpoint: string): Promise<Task[]> {
  const response = await fetch(endpoint);
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${response.statusText}`);
  }
  return response.json();
}

async function cancelTask(endpoint: string, taskId: string): Promise<void> {
  const response = await fetch(`${endpoint}/${taskId}/cancel`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(`Cancel failed: ${response.statusText}`);
  }
}

async function retryTask(endpoint: string, taskId: string): Promise<void> {
  const response = await fetch(`${endpoint}/${taskId}/retry`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(`Retry failed: ${response.statusText}`);
  }
}

export function TaskList({ endpoint, pollInterval = 5000, onTaskSelect, initialFilter }: TaskListProps) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [filter, setFilter] = useState<TaskFilter>(initialFilter ?? {});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sortField, setSortField] = useState<keyof Task>('created_at');
  const [sortDesc, setSortDesc] = useState(true);

  const loadTasks = useCallback(async () => {
    try {
      const data = await fetchTasks(endpoint);
      setTasks(data);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
    } finally {
      setLoading(false);
    }
  }, [endpoint]);

  useEffect(() => {
    loadTasks();
    const timer = setInterval(loadTasks, pollInterval);
    return () => clearInterval(timer);
  }, [loadTasks, pollInterval]);

  const filteredTasks = useMemo(
    () => filterTasks(tasks, filter),
    [tasks, filter]
  );

  const sortedTasks = useMemo(() => {
    const sorted = [...filteredTasks].sort((a, b) => {
      const aVal = a[sortField];
      const bVal = b[sortField];
      if (aVal === bVal) return 0;
      if (aVal === undefined || aVal === null) return 1;
      if (bVal === undefined || bVal === null) return -1;
      const cmp = aVal < bVal ? -1 : 1;
      return sortDesc ? -cmp : cmp;
    });
    return sorted;
  }, [filteredTasks, sortField, sortDesc]);

  const stats = useMemo(() => ({
    total: tasks.length,
    pending: tasks.filter(t => t.status === 'pending').length,
    running: tasks.filter(t => t.status === 'running').length,
    completed: tasks.filter(t => t.status === 'completed').length,
    failed: tasks.filter(t => t.status === 'failed').length,
  }), [tasks]);

  const handleSort = (field: keyof Task) => {
    if (field === sortField) {
      setSortDesc(!sortDesc);
    } else {
      setSortField(field);
      setSortDesc(true);
    }
  };

  const handleCancel = async (taskId: string) => {
    try {
      await cancelTask(endpoint, taskId);
      await loadTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Cancel failed');
    }
  };

  const handleRetry = async (taskId: string) => {
    try {
      await retryTask(endpoint, taskId);
      await loadTasks();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Retry failed');
    }
  };

  if (loading) {
    return <div className="task-list-loading">Loading tasks...</div>;
  }

  return (
    <div className="task-list">
      <div className="task-stats">
        <span>Total: {stats.total}</span>
        <span style={{ color: STATUS_COLORS.pending }}>Pending: {stats.pending}</span>
        <span style={{ color: STATUS_COLORS.running }}>Running: {stats.running}</span>
        <span style={{ color: STATUS_COLORS.completed }}>Done: {stats.completed}</span>
        <span style={{ color: STATUS_COLORS.failed }}>Failed: {stats.failed}</span>
      </div>

      {error && <div className="task-error">{error}</div>}

      <div className="task-filters">
        <input
          type="text"
          placeholder="Search tasks..."
          value={filter.search ?? ''}
          onChange={e => setFilter({ ...filter, search: e.target.value || undefined })}
        />
        <select
          value={filter.status ?? ''}
          onChange={e => setFilter({ ...filter, status: (e.target.value || undefined) as TaskStatus | undefined })}
        >
          <option value="">All statuses</option>
          <option value="pending">Pending</option>
          <option value="running">Running</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
        </select>
      </div>

      <table className="task-table">
        <thead>
          <tr>
            <th onClick={() => handleSort('id')}>ID</th>
            <th onClick={() => handleSort('type')}>Type</th>
            <th onClick={() => handleSort('priority')}>Priority</th>
            <th onClick={() => handleSort('status')}>Status</th>
            <th onClick={() => handleSort('created_at')}>Created</th>
            <th>Duration</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {sortedTasks.map(task => (
            <TaskRow
              key={task.id}
              task={task}
              onSelect={onTaskSelect}
              onCancel={handleCancel}
              onRetry={handleRetry}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

interface TaskRowProps {
  task: Task;
  onSelect?: (task: Task) => void;
  onCancel: (id: string) => void;
  onRetry: (id: string) => void;
}

function TaskRow({ task, onSelect, onCancel, onRetry }: TaskRowProps) {
  return (
    <tr onClick={() => onSelect?.(task)} className={`task-row task-${task.status}`}>
      <td className="task-id">{task.id.slice(0, 8)}</td>
      <td>{task.type}</td>
      <td>
        <span className={`priority priority-${task.priority}`}>
          {PRIORITY_LABELS[task.priority]}
        </span>
      </td>
      <td>
        <span style={{ color: STATUS_COLORS[task.status] }}>{task.status}</span>
        {task.retries > 0 && <span className="retry-count"> (retry {task.retries})</span>}
      </td>
      <td>{new Date(task.created_at).toLocaleTimeString()}</td>
      <td>{task.started_at ? formatDuration(task.started_at, task.done_at) : '—'}</td>
      <td className="task-actions">
        {task.status === 'pending' && (
          <button onClick={e => { e.stopPropagation(); onCancel(task.id); }}>Cancel</button>
        )}
        {task.status === 'failed' && task.retries < task.max_retry && (
          <button onClick={e => { e.stopPropagation(); onRetry(task.id); }}>Retry</button>
        )}
      </td>
    </tr>
  );
}

export default TaskList;
