#include "Stack.hpp"
#include <iostream>

int main() {
    Stack<int> s;
    s.push(1);
    s.push(2);
    std::cout << s.top() << " " << s.size() << std::endl;
    return 0;
}
