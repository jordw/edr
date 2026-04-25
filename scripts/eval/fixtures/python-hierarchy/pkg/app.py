from pkg.greeter import IGreeter, Hi, Loud


def run() -> str:
    g: IGreeter = Hi()
    l: Loud = Loud()
    return g.greet() + " " + l.greet()
