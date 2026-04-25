#ifndef STACK_HPP
#define STACK_HPP
#include <vector>

template <class T>
class Stack {
public:
    void push(T v) { data.push_back(v); }
    T top() const { return data.back(); }
    std::size_t size() const { return data.size(); }
private:
    std::vector<T> data;
};

#endif
