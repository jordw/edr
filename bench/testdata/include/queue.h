/**
 * task_queue - A thread-safe priority task queue with worker pool support.
 *
 * Designed for concurrent producer/consumer workloads where tasks are
 * dispatched by priority and processed by a pool of worker threads.
 */

#ifndef TASK_QUEUE_H
#define TASK_QUEUE_H

#include <pthread.h>
#include <stddef.h>
#include <time.h>

typedef enum {
    TASK_PENDING,
    TASK_RUNNING,
    TASK_COMPLETED,
    TASK_FAILED,
    TASK_CANCELLED
} task_status_t;

typedef enum {
    PRIORITY_LOW      = 0,
    PRIORITY_NORMAL   = 1,
    PRIORITY_HIGH     = 2,
    PRIORITY_CRITICAL = 3
} task_priority_t;

typedef struct task {
    char            id[64];
    char            type[32];
    task_priority_t priority;
    task_status_t   status;
    void           *payload;
    size_t          payload_size;
    time_t          created_at;
    int             retries;
    int             max_retries;
    char            error_msg[256];
} task_t;

typedef struct tq_stats {
    int pending;
    int running;
    int completed;
    int failed;
    int cancelled;
    int total;
} tq_stats_t;

typedef void (*task_handler_fn)(task_t *task, void *user_data);

typedef struct task_queue {
    task_t        **tasks;
    int             capacity;
    int             count;
    int             shutdown;
    pthread_mutex_t mutex;
    pthread_cond_t  cond;
} task_queue_t;

typedef struct worker {
    pthread_t       thread;
    int             id;
    int             running;
    task_queue_t   *queue;
    task_handler_fn handler;
    void           *user_data;
} worker_t;

/* Queue lifecycle */
task_queue_t *tq_create(int capacity);
void          tq_destroy(task_queue_t *q);

/* Queue operations */
int      tq_enqueue(task_queue_t *q, task_t *task);
task_t  *tq_dequeue(task_queue_t *q);
int      tq_cancel(task_queue_t *q, const char *task_id);
task_t  *tq_get(task_queue_t *q, const char *task_id);
int      tq_complete(task_queue_t *q, const char *task_id);
int      tq_fail(task_queue_t *q, const char *task_id, const char *error_msg);

/* Queue inspection */
tq_stats_t tq_stats(task_queue_t *q);
int        tq_count(task_queue_t *q);
int        tq_is_full(task_queue_t *q);
int        tq_is_empty(task_queue_t *q);

/* Worker pool */
worker_t *worker_create(int id, task_queue_t *q, task_handler_fn handler, void *user_data);
void      worker_destroy(worker_t *w);
int       worker_start(worker_t *w);
int       worker_stop(worker_t *w);

#endif /* TASK_QUEUE_H */
