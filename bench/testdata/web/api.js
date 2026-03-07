/**
 * Task Queue API — Express router, HTTP client, event emitter, and middleware.
 *
 * Provides a REST interface over a task queue backend, plus a client SDK
 * for programmatic access and a rate limiter for request throttling.
 *
 * @module task-queue/api
 */

import { EventEmitter } from "events";
import { Router } from "express";
import { randomUUID } from "crypto";

/* ------------------------------------------------------------------ */
/* HTTP Client                                                         */
/* ------------------------------------------------------------------ */

/**
 * SDK client for the task queue REST API.
 */
export class TaskAPIClient {
  /**
   * @param {string} baseURL  - API base (e.g. "http://localhost:3000/api/tasks")
   * @param {object} [opts]
   * @param {number} [opts.timeout=5000]        - Per-request timeout in ms.
   * @param {Record<string,string>} [opts.headers] - Extra headers for every request.
   */
  constructor(baseURL, opts = {}) {
    this.baseURL = baseURL.replace(/\/+$/, "");
    this.timeout = opts.timeout ?? 5000;
    this.headers = { "Content-Type": "application/json", ...opts.headers };
  }

  /** @private */
  async _request(method, path, body = undefined) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeout);

    try {
      const res = await fetch(`${this.baseURL}${path}`, {
        method,
        headers: this.headers,
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      if (!res.ok) {
        const detail = await res.text().catch(() => "");
        throw new Error(`API ${method} ${path} failed: ${res.status} — ${detail}`);
      }
      return res.json();
    } finally {
      clearTimeout(timer);
    }
  }

  /**
   * List tasks with optional filtering.
   * @param {{ status?: string, priority?: number, limit?: number, offset?: number }} [filter]
   */
  async listTasks(filter = {}) {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(filter)) {
      if (v !== undefined) params.set(k, String(v));
    }
    const qs = params.toString();
    return this._request("GET", qs ? `/?${qs}` : "/");
  }

  /** Fetch a single task by ID. */
  async getTask(id) {
    return this._request("GET", `/${encodeURIComponent(id)}`);
  }

  /** Create a new task and return the server-assigned record. */
  async createTask(payload) {
    return this._request("POST", "/", payload);
  }

  /** Cancel a pending task. Rejects if the task is already running. */
  async cancelTask(id) {
    return this._request("DELETE", `/${encodeURIComponent(id)}`);
  }

  /** Re-enqueue a failed task for another attempt. */
  async retryTask(id) {
    return this._request("POST", `/${encodeURIComponent(id)}/retry`);
  }

  /** Retrieve aggregate statistics (pending, running, completed, failed). */
  async getStats() {
    return this._request("GET", "/stats");
  }
}

/* ------------------------------------------------------------------ */
/* Event Emitter                                                       */
/* ------------------------------------------------------------------ */

/**
 * Emits lifecycle events as tasks transition between states.
 *
 * Events: "task:created", "task:started", "task:completed",
 *         "task:failed", "task:cancelled", "task:retried"
 */
export class TaskEventEmitter extends EventEmitter {
  constructor() {
    super();
    /** @type {Map<string, string>} task ID -> last known status */
    this._statusCache = new Map();
  }

  /**
   * Call this whenever a task's status changes.  Deduplicates by checking
   * against the last emitted status for the given task ID.
   */
  transition(task) {
    const prev = this._statusCache.get(task.id);
    if (prev === task.status) return;

    this._statusCache.set(task.id, task.status);

    const eventMap = {
      pending: "task:created",
      running: "task:started",
      completed: "task:completed",
      failed: "task:failed",
      cancelled: "task:cancelled",
    };

    const event = eventMap[task.status];
    if (event) {
      this.emit(event, { taskId: task.id, previous: prev ?? null, task });
    }
  }

  /** Remove tracking for a task (e.g. after archival). */
  forget(taskId) {
    this._statusCache.delete(taskId);
  }
}

/* ------------------------------------------------------------------ */
/* Validation                                                          */
/* ------------------------------------------------------------------ */

const ALLOWED_PRIORITIES = new Set(["low", "normal", "high", "critical"]);
const MAX_PAYLOAD_BYTES = 64 * 1024;

/**
 * Validate an incoming task creation payload.
 * Returns `{ valid: true, cleaned }` or `{ valid: false, errors: string[] }`.
 *
 * @param {object} payload
 */
export function validateTaskPayload(payload) {
  const errors = [];

  if (!payload || typeof payload !== "object") {
    return { valid: false, errors: ["Payload must be a JSON object."] };
  }

  if (typeof payload.type !== "string" || payload.type.trim().length === 0) {
    errors.push("Field 'type' is required and must be a non-empty string.");
  }

  if (payload.priority !== undefined && !ALLOWED_PRIORITIES.has(payload.priority)) {
    errors.push(`Invalid priority '${payload.priority}'. Allowed: ${[...ALLOWED_PRIORITIES].join(", ")}.`);
  }

  if (payload.data !== undefined) {
    const size = Buffer.byteLength(JSON.stringify(payload.data), "utf8");
    if (size > MAX_PAYLOAD_BYTES) {
      errors.push(`Payload data exceeds ${MAX_PAYLOAD_BYTES} bytes (got ${size}).`);
    }
  }

  if (payload.maxRetries !== undefined) {
    if (!Number.isInteger(payload.maxRetries) || payload.maxRetries < 0 || payload.maxRetries > 10) {
      errors.push("maxRetries must be an integer between 0 and 10.");
    }
  }

  if (errors.length > 0) return { valid: false, errors };

  return {
    valid: true,
    cleaned: {
      id: randomUUID(),
      type: payload.type.trim(),
      priority: payload.priority ?? "normal",
      data: payload.data ?? null,
      maxRetries: payload.maxRetries ?? 3,
    },
  };
}

/* ------------------------------------------------------------------ */
/* Rate Limiter                                                        */
/* ------------------------------------------------------------------ */

/**
 * Fixed-window rate limiter backed by an in-memory Map.
 */
export class RateLimiter {
  /**
   * @param {number} windowMs     - Window duration in milliseconds.
   * @param {number} maxRequests  - Max allowed requests per window per key.
   */
  constructor(windowMs = 60_000, maxRequests = 100) {
    this.windowMs = windowMs;
    this.maxRequests = maxRequests;
    /** @type {Map<string, { count: number, resetAt: number }>} */
    this.store = new Map();
  }

  /**
   * Check whether a request from `key` is allowed.
   * @returns {{ allowed: boolean, remaining: number, resetAt: number }}
   */
  check(key) {
    const now = Date.now();
    let entry = this.store.get(key);

    if (!entry || now >= entry.resetAt) {
      entry = { count: 0, resetAt: now + this.windowMs };
      this.store.set(key, entry);
    }

    entry.count++;
    const allowed = entry.count <= this.maxRequests;
    return {
      allowed,
      remaining: Math.max(0, this.maxRequests - entry.count),
      resetAt: entry.resetAt,
    };
  }

  /** Reset the window for a given key. */
  reset(key) {
    this.store.delete(key);
  }
}

/* ------------------------------------------------------------------ */
/* Express Router                                                      */
/* ------------------------------------------------------------------ */

/**
 * Build an Express router that exposes CRUD endpoints for a task queue.
 *
 * @param {object} queue          - Queue backend (e.g. an in-process queue or DB adapter).
 * @param {TaskEventEmitter} [emitter] - Optional emitter for lifecycle events.
 * @returns {Router}
 */
export function createTaskRouter(queue, emitter = null) {
  const router = Router();
  const limiter = new RateLimiter(60_000, 200);

  /** Rate-limit middleware keyed on IP. */
  router.use((req, res, next) => {
    const result = limiter.check(req.ip);
    res.set("X-RateLimit-Remaining", String(result.remaining));
    res.set("X-RateLimit-Reset", String(result.resetAt));
    if (!result.allowed) {
      return res.status(429).json({ error: "Too many requests." });
    }
    next();
  });

  router.get("/", async (req, res, next) => {
    try {
      const { status, priority, limit = 50, offset = 0 } = req.query;
      const tasks = await queue.list({ status, priority, limit: +limit, offset: +offset });
      res.json({ tasks, count: tasks.length });
    } catch (err) {
      next(err);
    }
  });

  router.get("/stats", async (_req, res, next) => {
    try {
      const stats = await queue.stats();
      res.json(stats);
    } catch (err) {
      next(err);
    }
  });

  router.get("/:id", async (req, res, next) => {
    try {
      const task = await queue.get(req.params.id);
      if (!task) return res.status(404).json({ error: "Task not found." });
      res.json(task);
    } catch (err) {
      next(err);
    }
  });

  router.post("/", async (req, res, next) => {
    try {
      const result = validateTaskPayload(req.body);
      if (!result.valid) {
        return res.status(400).json({ errors: result.errors });
      }
      const task = await queue.enqueue(result.cleaned);
      emitter?.transition({ ...task, status: "pending" });
      res.status(201).json(task);
    } catch (err) {
      next(err);
    }
  });

  router.delete("/:id", async (req, res, next) => {
    try {
      const cancelled = await queue.cancel(req.params.id);
      if (!cancelled) {
        return res.status(409).json({ error: "Task cannot be cancelled (not pending)." });
      }
      emitter?.transition({ id: req.params.id, status: "cancelled" });
      res.json({ ok: true });
    } catch (err) {
      next(err);
    }
  });

  router.post("/:id/retry", async (req, res, next) => {
    try {
      const task = await queue.retry(req.params.id);
      if (!task) {
        return res.status(404).json({ error: "Task not found or not in a retriable state." });
      }
      emitter?.transition({ ...task, status: "pending" });
      res.json(task);
    } catch (err) {
      next(err);
    }
  });

  return router;
}
