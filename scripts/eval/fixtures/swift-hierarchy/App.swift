public func run() -> String {
    let g: IGreeter = Hi()
    let l: Loud = Loud()
    return g.greet() + " " + l.greet()
}
