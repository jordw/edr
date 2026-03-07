module TaskQueue
  class Configuration
    attr_accessor :workers, :queue_size, :redis_url, :redis_options,
                  :log_level, :log_format, :retry_policy, :plugins

    def initialize
      @workers = 4
      @queue_size = 1000
      @redis_url = "redis://localhost:6379/0"
      @redis_options = {}
      @log_level = :info
      @log_format = :text
      @retry_policy = RetryPolicy.new
      @plugins = PluginRegistry.new
    end

    def validate!
      raise ArgumentError, "workers must be > 0" unless @workers.positive?
      raise ArgumentError, "queue_size must be > 0" unless @queue_size.positive?
      raise ArgumentError, "redis_url is required" if @redis_url.nil? || @redis_url.empty?
      self
    end

    def to_h
      {
        workers: @workers, queue_size: @queue_size,
        redis_url: @redis_url, log_level: @log_level,
        retry_policy: @retry_policy.to_h,
        plugins: @plugins.all.map(&:name)
      }
    end
  end

  class RetryPolicy
    attr_accessor :max_retries, :backoff_type, :base_delay, :max_delay, :retry_on

    def initialize
      @max_retries = 3
      @backoff_type = :exponential
      @base_delay = 1.0
      @max_delay = 30.0
      @retry_on = [StandardError]
    end

    def delay_for(attempt)
      raw = case @backoff_type
            when :linear
              @base_delay * attempt
            when :exponential
              @base_delay * (2**(attempt - 1))
            else
              raise ArgumentError, "Unknown backoff type: #{@backoff_type}"
            end

      [raw, @max_delay].min
    end

    def should_retry?(exception, attempt)
      return false if attempt >= @max_retries
      @retry_on.any? { |klass| exception.is_a?(klass) }
    end

    def to_h
      {
        max_retries: @max_retries, backoff_type: @backoff_type,
        base_delay: @base_delay, max_delay: @max_delay,
        retry_on: @retry_on.map(&:name)
      }
    end
  end

  class ConfigDSL
    def initialize(config)
      @config = config
    end

    def workers(n)
      @config.workers = Integer(n)
    end

    def queue_size(n)
      @config.queue_size = Integer(n)
    end

    def redis(url, **opts)
      @config.redis_url = url
      @config.redis_options = opts
    end

    def logging(level:, format: :text)
      @config.log_level = level.to_sym
      @config.log_format = format.to_sym
    end

    def retry_policy(&block)
      policy = RetryPolicy.new
      RetryPolicyDSL.new(policy).instance_eval(&block)
      @config.retry_policy = policy
    end

    def plugin(name, **opts)
      @config.plugins.register(name, opts)
    end
  end

  class RetryPolicyDSL
    def initialize(policy)
      @policy = policy
    end

    def max_retries(n)
      @policy.max_retries = Integer(n)
    end

    def backoff(type, base: 1.0, max: 30.0)
      @policy.backoff_type = type.to_sym
      @policy.base_delay = Float(base)
      @policy.max_delay = Float(max)
    end

    def retry_on(*exceptions)
      @policy.retry_on = exceptions.flatten
    end
  end

  class PluginRegistry
    PluginEntry = Struct.new(:name, :options, :instance, keyword_init: true)

    def initialize
      @plugins = {}
    end

    def register(name, options = {})
      key = name.to_sym
      raise ArgumentError, "Plugin already registered: #{key}" if @plugins.key?(key)
      @plugins[key] = PluginEntry.new(name: key, options: options, instance: nil)
    end

    def get(name)
      @plugins.fetch(name.to_sym) { raise KeyError, "Unknown plugin: #{name}" }
    end

    def all
      @plugins.values
    end

    def loaded?(name)
      entry = @plugins[name.to_sym]
      entry&.instance != nil
    end
  end

  @configuration = Configuration.new

  def self.configure(&block)
    dsl = ConfigDSL.new(@configuration)
    dsl.instance_eval(&block)
    @configuration.validate!
    @configuration
  end

  def self.configuration
    @configuration
  end

  def self.reset!
    @configuration = Configuration.new
  end
end
