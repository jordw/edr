package pkg_b

import "fmt"

// Process uses a local variable 'config' that shadows the type name Config.
func Process() string {
	config := Init()
	config.Name = "processed"
	return fmt.Sprintf("processed: %s v%s", config.Name, config.Version)
}

// UseConfig takes a Config parameter — this is a real reference to the type.
func UseConfig(c *Config) string {
	return c.Name + " " + c.Version
}

// Merge combines two Config values.
func Merge(a, b *Config) *Config {
	result := &Config{
		Name:     a.Name,
		Version:  b.Version,
		Replicas: a.Replicas + b.Replicas,
	}
	return result
}
