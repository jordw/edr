public protocol Greeter {
    func greet() -> String
}

extension Greeter {
    public func greet() -> String {
        return "hello"
    }
}
