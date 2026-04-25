namespace Demo;

public interface IGreeter {
    string Greet();
}

public class Hi : IGreeter {
    public virtual string Greet() => "hi";
}

public class Loud : Hi {
    public override string Greet() => "HI!";
}
