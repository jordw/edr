#ifndef GREETER_HPP
#define GREETER_HPP
#include <string>

class IGreeter {
public:
    virtual std::string greet() const = 0;
    virtual ~IGreeter() = default;
};

class Hi : public IGreeter {
public:
    std::string greet() const override;
};

class Loud : public Hi {
public:
    std::string greet() const override;
};

#endif
