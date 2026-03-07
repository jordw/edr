/**
 * task_queue implementation — thread-safe priority queue with worker threads.
 */

#include "../include/queue.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <errno.h>

/* ------------------------------------------------------------------ */
/* Queue lifecycle                                                     */
/* ------------------------------------------------------------------ */

task_queue_t *tq_create(int capacity) {
    if (capacity <= 0) {
        return NULL;
    }

    task_queue_t *q = calloc(1, sizeof(task_queue_t));
    if (!q) {
        return NULL;
    }

    q->tasks = calloc((size_t)capacity, sizeof(task_t *));
    if (!q->tasks) {
        free(q);
        return NULL;
    }

    q->capacity = capacity;
    q->count    = 0;
    q->shutdown = 0;

    if (pthread_mutex_init(&q->mutex, NULL) != 0) {
        free(q->tasks);
        free(q);
        return NULL;
    }
    if (pthread_cond_init(&q->cond, NULL) != 0) {
        pthread_mutex_destroy(&q->mutex);
        free(q->tasks);
        free(q);
        return NULL;
    }

    return q;
}

void tq_destroy(task_queue_t *q) {
    if (!q) return;

    pthread_mutex_lock(&q->mutex);
    q->shutdown = 1;
    pthread_cond_broadcast(&q->cond);
    pthread_mutex_unlock(&q->mutex);

    /* Free remaining tasks and their payloads. */
    for (int i = 0; i < q->count; i++) {
        if (q->tasks[i]) {
            free(q->tasks[i]->payload);
            free(q->tasks[i]);
        }
    }

    free(q->tasks);
    pthread_mutex_destroy(&q->mutex);
    pthread_cond_destroy(&q->cond);
    free(q);
}

/* ------------------------------------------------------------------ */
/* Internal helpers                                                    */
/* ------------------------------------------------------------------ */

static int find_task_index(task_queue_t *q, const char *task_id) {
    for (int i = 0; i < q->count; i++) {
        if (q->tasks[i] && strcmp(q->tasks[i]->id, task_id) == 0) {
            return i;
        }
    }
    return -1;
}

/* Insert task in priority-sorted order (highest priority at the front). */
static void insert_sorted(task_queue_t *q, task_t *task) {
    int pos = q->count;
    for (int i = 0; i < q->count; i++) {
        if (task->priority > q->tasks[i]->priority) {
            pos = i;
            break;
        }
    }
    /* Shift everything after pos to the right. */
    memmove(&q->tasks[pos + 1], &q->tasks[pos],
            (size_t)(q->count - pos) * sizeof(task_t *));
    q->tasks[pos] = task;
    q->count++;
}

/* ------------------------------------------------------------------ */
/* Queue operations                                                    */
/* ------------------------------------------------------------------ */

int tq_enqueue(task_queue_t *q, task_t *task) {
    if (!q || !task) return -1;

    pthread_mutex_lock(&q->mutex);

    if (q->count >= q->capacity) {
        pthread_mutex_unlock(&q->mutex);
        return -1;
    }

    task->status     = TASK_PENDING;
    task->created_at = time(NULL);

    insert_sorted(q, task);

    pthread_cond_signal(&q->cond);
    pthread_mutex_unlock(&q->mutex);
    return 0;
}

task_t *tq_dequeue(task_queue_t *q) {
    if (!q) return NULL;

    pthread_mutex_lock(&q->mutex);

    while (q->count == 0 && !q->shutdown) {
        pthread_cond_wait(&q->cond, &q->mutex);
    }

    if (q->shutdown) {
        pthread_mutex_unlock(&q->mutex);
        return NULL;
    }

    /* The front of the array has the highest priority pending task. */
    task_t *task = NULL;
    for (int i = 0; i < q->count; i++) {
        if (q->tasks[i]->status == TASK_PENDING) {
            task = q->tasks[i];
            task->status = TASK_RUNNING;
            /* Remove from array, shift left. */
            memmove(&q->tasks[i], &q->tasks[i + 1],
                    (size_t)(q->count - i - 1) * sizeof(task_t *));
            q->count--;
            break;
        }
    }

    pthread_mutex_unlock(&q->mutex);
    return task;
}

int tq_complete(task_queue_t *q, const char *task_id) {
    if (!q || !task_id) return -1;

    pthread_mutex_lock(&q->mutex);
    int idx = find_task_index(q, task_id);
    if (idx < 0) {
        pthread_mutex_unlock(&q->mutex);
        return -1;
    }
    q->tasks[idx]->status = TASK_COMPLETED;
    pthread_mutex_unlock(&q->mutex);
    return 0;
}

int tq_fail(task_queue_t *q, const char *task_id, const char *error_msg) {
    if (!q || !task_id) return -1;

    pthread_mutex_lock(&q->mutex);
    int idx = find_task_index(q, task_id);
    if (idx < 0) {
        pthread_mutex_unlock(&q->mutex);
        return -1;
    }

    task_t *t = q->tasks[idx];
    t->retries++;

    if (t->retries < t->max_retries) {
        /* Re-enqueue for retry — reset to pending. */
        t->status = TASK_PENDING;
    } else {
        t->status = TASK_FAILED;
    }

    if (error_msg) {
        strncpy(t->error_msg, error_msg, sizeof(t->error_msg) - 1);
        t->error_msg[sizeof(t->error_msg) - 1] = '\0';
    }

    pthread_mutex_unlock(&q->mutex);
    return 0;
}

int tq_cancel(task_queue_t *q, const char *task_id) {
    if (!q || !task_id) return -1;

    pthread_mutex_lock(&q->mutex);
    int idx = find_task_index(q, task_id);
    if (idx < 0 || q->tasks[idx]->status != TASK_PENDING) {
        pthread_mutex_unlock(&q->mutex);
        return -1;
    }

    q->tasks[idx]->status = TASK_CANCELLED;
    pthread_mutex_unlock(&q->mutex);
    return 0;
}

task_t *tq_get(task_queue_t *q, const char *task_id) {
    if (!q || !task_id) return NULL;

    pthread_mutex_lock(&q->mutex);
    int idx = find_task_index(q, task_id);
    task_t *result = (idx >= 0) ? q->tasks[idx] : NULL;
    pthread_mutex_unlock(&q->mutex);
    return result;
}

/* ------------------------------------------------------------------ */
/* Queue inspection                                                    */
/* ------------------------------------------------------------------ */

tq_stats_t tq_stats(task_queue_t *q) {
    tq_stats_t stats = {0};
    if (!q) return stats;

    pthread_mutex_lock(&q->mutex);
    for (int i = 0; i < q->count; i++) {
        switch (q->tasks[i]->status) {
            case TASK_PENDING:   stats.pending++;   break;
            case TASK_RUNNING:   stats.running++;   break;
            case TASK_COMPLETED: stats.completed++; break;
            case TASK_FAILED:    stats.failed++;    break;
            case TASK_CANCELLED: stats.cancelled++; break;
        }
    }
    stats.total = q->count;
    pthread_mutex_unlock(&q->mutex);
    return stats;
}

int tq_count(task_queue_t *q) {
    if (!q) return 0;
    pthread_mutex_lock(&q->mutex);
    int n = q->count;
    pthread_mutex_unlock(&q->mutex);
    return n;
}

int tq_is_full(task_queue_t *q) {
    if (!q) return 0;
    pthread_mutex_lock(&q->mutex);
    int full = (q->count >= q->capacity);
    pthread_mutex_unlock(&q->mutex);
    return full;
}

int tq_is_empty(task_queue_t *q) {
    if (!q) return 1;
    pthread_mutex_lock(&q->mutex);
    int empty = (q->count == 0);
    pthread_mutex_unlock(&q->mutex);
    return empty;
}

/* ------------------------------------------------------------------ */
/* Worker pool                                                         */
/* ------------------------------------------------------------------ */

static void *worker_loop(void *arg) {
    worker_t *w = (worker_t *)arg;

    while (w->running) {
        task_t *task = tq_dequeue(w->queue);
        if (!task) {
            /* Queue shut down. */
            break;
        }
        if (w->handler) {
            w->handler(task, w->user_data);
        }
    }

    return NULL;
}

worker_t *worker_create(int id, task_queue_t *q, task_handler_fn handler, void *user_data) {
    if (!q || !handler) return NULL;

    worker_t *w = calloc(1, sizeof(worker_t));
    if (!w) return NULL;

    w->id        = id;
    w->queue     = q;
    w->handler   = handler;
    w->user_data = user_data;
    w->running   = 0;
    return w;
}

void worker_destroy(worker_t *w) {
    if (!w) return;
    if (w->running) {
        worker_stop(w);
    }
    free(w);
}

int worker_start(worker_t *w) {
    if (!w || w->running) return -1;
    w->running = 1;
    if (pthread_create(&w->thread, NULL, worker_loop, w) != 0) {
        w->running = 0;
        return -1;
    }
    return 0;
}

int worker_stop(worker_t *w) {
    if (!w || !w->running) return -1;
    w->running = 0;
    /* Signal the queue condition to unblock a waiting dequeue. */
    pthread_mutex_lock(&w->queue->mutex);
    pthread_cond_broadcast(&w->queue->cond);
    pthread_mutex_unlock(&w->queue->mutex);
    pthread_join(w->thread, NULL);
    return 0;
}
