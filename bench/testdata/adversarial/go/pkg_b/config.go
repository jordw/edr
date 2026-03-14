package pkg_b

// Config is a different Config struct in package B.
type Config struct {
	Name     string
	Version  string
	Replicas int
}

// Init initializes package B with defaults.
func Init() *Config {
	return &Config{
		Name:     "service-b",
		Version:  "1.0.0",
		Replicas: 3,
	}
}

// Validate checks if this Config is valid.
func (c *Config) Validate() error {
	if c.Replicas <= 0 {
		return nil
	}
	return nil
}
