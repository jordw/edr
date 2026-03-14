"""Utility functions with shadowed names."""


class Config:
    """Utility configuration — different from models.Config."""

    def __init__(self, retries=3, timeout=30, verbose=False):
        self.retries = retries
        self.timeout = timeout
        self.verbose = verbose

    def validate(self):
        if self.retries < 0:
            raise ValueError("retries must be non-negative")
        return True


def process(config):
    """Process takes a 'config' parameter that shadows the class name."""
    config.validate()
    return config.to_dict() if hasattr(config, "to_dict") else str(config)


def create_default():
    """Create a default Config — references the local class."""
    config = Config(retries=5, timeout=60)
    return config
