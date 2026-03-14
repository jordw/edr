"""Data models for the application."""


class Config:
    """Application configuration from models module."""

    def __init__(self, host="localhost", port=8080, debug=False):
        self.host = host
        self.port = port
        self.debug = debug

    def validate(self):
        if self.port <= 0:
            raise ValueError("port must be positive")
        return True

    def to_dict(self):
        return {"host": self.host, "port": self.port, "debug": self.debug}


class Result:
    """Generic result wrapper."""

    def __init__(self, value, error=None):
        self.value = value
        self.error = error

    def is_ok(self):
        return self.error is None
