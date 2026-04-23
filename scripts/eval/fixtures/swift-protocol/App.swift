struct World: Greeter {}

public func run() -> String {
    let w: Greeter = World()
    return w.greet()
}
