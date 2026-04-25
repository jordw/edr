#include "Shapes.hpp"
#include <iostream>
#include <string>

int main() {
    Widget w;
    Drawable* d = &w;
    Loggable* l = &w;
    std::string a = d->draw();
    std::string b = l->log_msg();
    std::string c = w.draw();
    std::cout << a << " " << b << " " << c << std::endl;
    return 0;
}
