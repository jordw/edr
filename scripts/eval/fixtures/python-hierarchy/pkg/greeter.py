from abc import ABC, abstractmethod


class IGreeter(ABC):
    @abstractmethod
    def greet(self) -> str:
        ...


class Hi(IGreeter):
    def greet(self) -> str:
        return "hi"


class Loud(Hi):
    def greet(self) -> str:
        return "HI!"
