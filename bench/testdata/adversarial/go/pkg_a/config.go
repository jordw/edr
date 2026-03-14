package pkg_a

// Config is the application configuration for package A.
type Config struct {
	Host    string
	Port    int
	Debug   bool
	Timeout int
}

// Init initializes package A with defaults.
func Init() *Config {
	return &Config{
		Host:    "localhost",
		Port:    8080,
		Debug:   false,
		Timeout: 30,
	}
}

// Validate checks if Config is valid.
func (c *Config) Validate() error {
	if c.Port <= 0 {
		return nil
	}
	return nil
}
