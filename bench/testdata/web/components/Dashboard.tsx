import React, { useState, useEffect, useRef } from 'react';
import { TaskList } from './TaskList';

interface QueueStats {
  pending: number;
  running: number;
  completed: number;
  failed: number;
  avg_wait_ms: number;
}

interface WorkerInfo {
  id: number;
  tasks_processed: number;
  tasks_failed: number;
  total_duration_ms: number;
  last_task_at?: string;
}

interface DashboardProps {
  apiBase: string;
  refreshRate?: number;
}

function usePolling<T>(url: string, interval: number, initial: T): [T, string | null] {
  const [data, setData] = useState<T>(initial);
  const [error, setError] = useState<string | null>(null);
  const savedUrl = useRef(url);
  savedUrl.current = url;

  useEffect(() => {
    let active = true;
    const poll = async () => {
      try {
        const res = await fetch(savedUrl.current);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = await res.json();
        if (active) {
          setData(json);
          setError(null);
        }
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : 'fetch error');
      }
    };
    poll();
    const timer = setInterval(poll, interval);
    return () => { active = false; clearInterval(timer); };
  }, [interval]);

  return [data, error];
}

function formatUptime(startMs: number): string {
  const elapsed = Date.now() - startMs;
  const hours = Math.floor(elapsed / 3600000);
  const minutes = Math.floor((elapsed % 3600000) / 60000);
  const seconds = Math.floor((elapsed % 60000) / 1000);
  return `${hours}h ${minutes}m ${seconds}s`;
}

function StatsPanel({ stats, error }: { stats: QueueStats; error: string | null }) {
  const total = stats.pending + stats.running + stats.completed + stats.failed;
  const successRate = total > 0 ? ((stats.completed / total) * 100).toFixed(1) : '0.0';

  return (
    <div className="stats-panel">
      <h2>Queue Stats</h2>
      {error && <div className="error-banner">{error}</div>}
      <div className="stat-grid">
        <div className="stat">
          <span className="stat-value">{stats.pending}</span>
          <span className="stat-label">Pending</span>
        </div>
        <div className="stat">
          <span className="stat-value">{stats.running}</span>
          <span className="stat-label">Running</span>
        </div>
        <div className="stat">
          <span className="stat-value">{stats.completed}</span>
          <span className="stat-label">Completed</span>
        </div>
        <div className="stat">
          <span className="stat-value">{stats.failed}</span>
          <span className="stat-label">Failed</span>
        </div>
        <div className="stat">
          <span className="stat-value">{successRate}%</span>
          <span className="stat-label">Success Rate</span>
        </div>
        <div className="stat">
          <span className="stat-value">{stats.avg_wait_ms}ms</span>
          <span className="stat-label">Avg Wait</span>
        </div>
      </div>
    </div>
  );
}

function WorkerTable({ workers }: { workers: WorkerInfo[] }) {
  return (
    <div className="worker-table">
      <h2>Workers</h2>
      <table>
        <thead>
          <tr>
            <th>ID</th>
            <th>Processed</th>
            <th>Failed</th>
            <th>Avg Time</th>
            <th>Last Active</th>
          </tr>
        </thead>
        <tbody>
          {workers.map(w => {
            const avgMs = w.tasks_processed > 0
              ? Math.round(w.total_duration_ms / w.tasks_processed)
              : 0;
            return (
              <tr key={w.id}>
                <td>Worker {w.id}</td>
                <td>{w.tasks_processed}</td>
                <td>{w.tasks_failed}</td>
                <td>{avgMs}ms</td>
                <td>{w.last_task_at ? new Date(w.last_task_at).toLocaleTimeString() : '—'}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function Dashboard({ apiBase, refreshRate = 3000 }: DashboardProps) {
  const [stats, statsError] = usePolling<QueueStats>(
    `${apiBase}/stats`,
    refreshRate,
    { pending: 0, running: 0, completed: 0, failed: 0, avg_wait_ms: 0 }
  );
  const [workers, workersError] = usePolling<WorkerInfo[]>(
    `${apiBase}/workers`,
    refreshRate * 2,
    []
  );
  const [startTime] = useState(Date.now());
  const [uptime, setUptime] = useState('0h 0m 0s');

  useEffect(() => {
    const timer = setInterval(() => setUptime(formatUptime(startTime)), 1000);
    return () => clearInterval(timer);
  }, [startTime]);

  return (
    <div className="dashboard">
      <header className="dashboard-header">
        <h1>Task Queue Dashboard</h1>
        <span className="uptime">Uptime: {uptime}</span>
      </header>
      <StatsPanel stats={stats} error={statsError} />
      <WorkerTable workers={workers} />
      <TaskList endpoint={`${apiBase}/tasks`} pollInterval={refreshRate} />
    </div>
  );
}

export default Dashboard;
