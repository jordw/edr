package lib;

import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.logging.Logger;

public class TaskProcessor {

    public enum TaskPriority {
        LOW(0),
        NORMAL(10),
        HIGH(50),
        CRITICAL(100);

        private final int value;

        TaskPriority(int value) {
            this.value = value;
        }

        public int getValue() {
            return value;
        }

        public int compareTo(TaskPriority other) {
            return Integer.compare(this.value, other.value);
        }

        public boolean isHigherThan(TaskPriority other) {
            return this.value > other.value;
        }
    }

    public interface TaskHandler {
        TaskResult handle(TaskContext ctx) throws Exception;
    }

    public record TaskResult(boolean success, Object data, String error) {
        public static TaskResult ok(Object data) {
            return new TaskResult(true, data, null);
        }

        public static TaskResult fail(String error) {
            return new TaskResult(false, null, error);
        }

        public boolean isRetryable() {
            return !success && error != null && !error.startsWith("FATAL:");
        }
    }

    public static class TaskContext {
        private final String taskId;
        private final String taskType;
        private final Map<String, Object> payload;
        private int attempt;
        private final int maxAttempts;
        private final Instant deadline;
        private final Logger logger;

        public TaskContext(String taskId, String taskType, Map<String, Object> payload,
                           int maxAttempts, Duration timeout) {
            this.taskId = taskId;
            this.taskType = taskType;
            this.payload = new ConcurrentHashMap<>(payload);
            this.attempt = 1;
            this.maxAttempts = maxAttempts;
            this.deadline = Instant.now().plus(timeout);
            this.logger = Logger.getLogger("TaskProcessor." + taskType);
        }

        public String getTaskId() { return taskId; }
        public String getTaskType() { return taskType; }
        public Map<String, Object> getPayload() { return payload; }
        public int getAttempt() { return attempt; }
        public int getMaxAttempts() { return maxAttempts; }
        public Instant getDeadline() { return deadline; }
        public Logger getLogger() { return logger; }

        void incrementAttempt() { this.attempt++; }

        public boolean isExpired() {
            return Instant.now().isAfter(deadline);
        }

        public Duration remainingTime() {
            Duration remaining = Duration.between(Instant.now(), deadline);
            return remaining.isNegative() ? Duration.ZERO : remaining;
        }

        @SuppressWarnings("unchecked")
        public <T> T getPayloadAs(String key, Class<T> type) {
            Object value = payload.get(key);
            if (value == null) {
                return null;
            }
            if (!type.isInstance(value)) {
                throw new ClassCastException(
                    "Payload key '" + key + "' is " + value.getClass().getSimpleName()
                    + ", expected " + type.getSimpleName()
                );
            }
            return (T) value;
        }
    }

    public record ProcessorMetrics(long submitted, long completed, long failed, double avgDurationMs) {
        public double successRate() {
            long total = completed + failed;
            return total == 0 ? 0.0 : (double) completed / total;
        }

        @Override
        public String toString() {
            return String.format("Metrics{submitted=%d, completed=%d, failed=%d, avgMs=%.2f, rate=%.1f%%}",
                submitted, completed, failed, avgDurationMs, successRate() * 100);
        }
    }

    private final ExecutorService executor;
    private final BlockingQueue<TaskContext> queue;
    private final ConcurrentHashMap<String, TaskHandler> handlers;
    private final AtomicBoolean running;
    private final AtomicLong submittedCount;
    private final AtomicLong completedCount;
    private final AtomicLong failedCount;
    private final AtomicLong totalDurationNanos;
    private final int concurrency;

    public TaskProcessor(int concurrency, int queueCapacity) {
        this.concurrency = concurrency;
        this.executor = Executors.newFixedThreadPool(concurrency, r -> {
            Thread t = new Thread(r, "task-worker");
            t.setDaemon(true);
            return t;
        });
        this.queue = new PriorityBlockingQueue<>(queueCapacity,
            (a, b) -> 0  // FIFO within priority; real impl would compare priority fields
        );
        this.handlers = new ConcurrentHashMap<>();
        this.running = new AtomicBoolean(false);
        this.submittedCount = new AtomicLong(0);
        this.completedCount = new AtomicLong(0);
        this.failedCount = new AtomicLong(0);
        this.totalDurationNanos = new AtomicLong(0);
    }

    public void registerHandler(String taskType, TaskHandler handler) {
        if (handler == null) {
            throw new IllegalArgumentException("Handler cannot be null for type: " + taskType);
        }
        handlers.put(taskType, handler);
    }

    public CompletableFuture<TaskResult> submit(String taskType, Map<String, Object> payload,
                                                 int maxAttempts, Duration timeout) {
        TaskHandler handler = handlers.get(taskType);
        if (handler == null) {
            return CompletableFuture.completedFuture(
                TaskResult.fail("No handler registered for task type: " + taskType)
            );
        }

        String taskId = taskType + "-" + System.nanoTime();
        TaskContext ctx = new TaskContext(taskId, taskType, payload, maxAttempts, timeout);
        submittedCount.incrementAndGet();

        return CompletableFuture.supplyAsync(() -> processWithRetry(handler, ctx), executor);
    }


    private TaskResult processWithRetry(TaskHandler handler, TaskContext ctx) {
        while (ctx.getAttempt() <= ctx.getMaxAttempts()) {
            if (ctx.isExpired()) {
                failedCount.incrementAndGet();
                return TaskResult.fail("Task " + ctx.getTaskId() + " expired after "
                    + ctx.getAttempt() + " attempts");
            }

            long startNanos = System.nanoTime();
            try {
                TaskResult result = handler.handle(ctx);
                long elapsed = System.nanoTime() - startNanos;
                totalDurationNanos.addAndGet(elapsed);

                if (result.success()) {
                    completedCount.incrementAndGet();
                    return result;
                }

                if (!result.isRetryable() || ctx.getAttempt() >= ctx.getMaxAttempts()) {
                    failedCount.incrementAndGet();
                    return result;
                }

                ctx.getLogger().warning("Task " + ctx.getTaskId()
                    + " failed (attempt " + ctx.getAttempt() + "): " + result.error());
                ctx.incrementAttempt();
                backoff(ctx.getAttempt());

            } catch (Exception e) {
                long elapsed = System.nanoTime() - startNanos;
                totalDurationNanos.addAndGet(elapsed);
                failedCount.incrementAndGet();
                return TaskResult.fail("Exception: " + e.getMessage());
            }
        }
        failedCount.incrementAndGet();
        return TaskResult.fail("Exhausted retries for task " + ctx.getTaskId());
    }

    private void backoff(int attempt) {
        try {
            long delayMs = Math.min(100L * (1L << attempt), 5000L);
            Thread.sleep(delayMs);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    public ProcessorMetrics getMetrics() {
        long completed = completedCount.get();
        long failed = failedCount.get();
        long totalNanos = totalDurationNanos.get();
        long totalTasks = completed + failed;
        double avgMs = totalTasks == 0 ? 0.0 : (totalNanos / 1_000_000.0) / totalTasks;

        return new ProcessorMetrics(submittedCount.get(), completed, failed, avgMs);
    }

    public void shutdown() {
        running.set(false);
        executor.shutdown();
        try {
            if (!executor.awaitTermination(10, TimeUnit.SECONDS)) {
                executor.shutdownNow();
            }
        } catch (InterruptedException e) {
            executor.shutdownNow();
            Thread.currentThread().interrupt();
        }
    }
}
