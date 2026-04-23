struct World: Greeter {}

public func run() -> String {
    return World().greet()
}
