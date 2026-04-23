#include "Counter.hpp"
#include <iostream>

int main() {
    Counter c;
    int r = c.value();
    std::cout << r << std::endl;
    return 0;
}
