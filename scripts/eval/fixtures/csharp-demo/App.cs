namespace Demo;

public static class App
{
    public static int Run() => Lib.Compute(5);

    public static void Main() => System.Console.WriteLine(Run());
}
