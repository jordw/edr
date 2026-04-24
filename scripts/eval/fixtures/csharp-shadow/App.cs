namespace Demo;

public static class App {
    public static int Run() {
        var result = Lib.Compute(5);
        int Compute = 42;
        return result + Compute;
    }
    public static void Main() => System.Console.WriteLine(Run());
}
