package demo

fun run(): Int {
    val c = Counter()
    return c.value()
}

fun main() { println(run()) }
