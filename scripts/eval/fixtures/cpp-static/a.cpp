#include <iostream>

static int helper() { return 1; }

int use_a() { return helper(); }

int main() { std::cout << use_a() << std::endl; return 0; }
