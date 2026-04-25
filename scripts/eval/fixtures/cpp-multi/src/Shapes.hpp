#ifndef SHAPES_HPP
#define SHAPES_HPP
#include <string>

class Drawable {
public:
    virtual std::string draw() const = 0;
    virtual ~Drawable() = default;
};

class Loggable {
public:
    virtual std::string log_msg() const = 0;
    virtual ~Loggable() = default;
};

class Widget : public Drawable, public Loggable {
public:
    std::string draw() const override;
    std::string log_msg() const override;
};

#endif
