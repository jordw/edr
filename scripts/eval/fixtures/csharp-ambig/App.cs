namespace Demo;

public static class App {
    public static int Run() => A.Compute(5) + B.Compute(3);
    public static void Main() => System.Console.WriteLine(Run());
}
