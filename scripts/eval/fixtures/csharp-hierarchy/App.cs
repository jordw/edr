namespace Demo;

public static class App {
    public static string Run() {
        IGreeter g = new Hi();
        Loud l = new Loud();
        return g.Greet() + " " + l.Greet();
    }
    public static void Main() => System.Console.WriteLine(Run());
}
