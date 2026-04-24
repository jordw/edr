namespace Demo;

public static class App {
    public static int Run() {
        var c = new Counter();
        return c.Value();
    }
    public static void Main() => System.Console.WriteLine(Run());
}
