package main

import (
	"fmt"

	cfgA "example.com/adversarial/pkg_a"
	cfgB "example.com/adversarial/pkg_b"
)

// Start uses aliased imports to both Config types.
func Start() {
	a := cfgA.Init()
	b := cfgB.Init()

	fmt.Println(a.Host, a.Port)
	fmt.Println(b.Name, b.Version)

	if err := a.Validate(); err != nil {
		fmt.Println("invalid a:", err)
	}
	if err := b.Validate(); err != nil {
		fmt.Println("invalid b:", err)
	}
}

// Configure creates both configs and returns them.
func Configure() (*cfgA.Config, *cfgB.Config) {
	return cfgA.Init(), cfgB.Init()
}

func main() {
	Start()
}
