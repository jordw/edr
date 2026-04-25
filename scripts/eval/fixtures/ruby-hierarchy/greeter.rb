module IGreeter
  def greet
    "default"
  end
end

class Hi
  include IGreeter
end

class Loud < Hi
  def greet
    "HI!"
  end
end
