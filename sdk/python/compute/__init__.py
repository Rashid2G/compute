"""
Compute Python SDK
Official client library for managing isolated microVM sandboxes and running AI code.
"""

from .sandbox import Sandbox, ExecutionResult
from .exceptions import ComputeError, SandboxNotFoundError, AuthenticationError

__version__ = "0.2.0"
__all__ = ["Sandbox", "ExecutionResult", "ComputeError", "SandboxNotFoundError", "AuthenticationError"]
