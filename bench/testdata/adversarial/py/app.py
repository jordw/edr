"""Application entry point using aliased imports."""

from .models import Config as ModelConfig
from .utils import Config as UtilConfig
from .models import Result


def setup():
    """Set up app using both Config types via aliases."""
    model_cfg = ModelConfig(host="0.0.0.0", port=9090, debug=True)
    util_cfg = UtilConfig(retries=10, timeout=120)

    model_cfg.validate()
    util_cfg.validate()

    return model_cfg, util_cfg


def run(host, port):
    """Run the application."""
    cfg = ModelConfig(host=host, port=port)
    result = Result(value=cfg.to_dict())
    return result


def quick_config():
    """Create a util config quickly."""
    return UtilConfig(retries=1, timeout=5, verbose=True)
