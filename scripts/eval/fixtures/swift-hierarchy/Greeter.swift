public protocol IGreeter {
    func greet() -> String
}

public class Hi: IGreeter {
    public func greet() -> String { return "hi" }
}

public class Loud: Hi {
    public override func greet() -> String { return "HI!" }
}
