#include "Greeter.hpp"
#include <iostream>
#include <memory>

int main() {
    std::unique_ptr<IGreeter> g = std::make_unique<Hi>();
    Loud l;
    int r1 = g->greet().length();
    int r2 = l.greet().length();
    std::cout << r1 << " " << r2 << std::endl;
    return 0;
}
