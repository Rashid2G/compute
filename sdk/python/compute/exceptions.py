class ComputeError(Exception):
    """Base exception for all Compute SDK errors."""
    pass

class AuthenticationError(ComputeError):
    """Raised when an API key is missing, invalid, or expired."""
    pass

class SandboxNotFoundError(ComputeError):
    """Raised when a sandbox ID or name does not exist or has already been terminated."""
    pass

class CommandExecutionError(ComputeError):
    """Raised when a command execution inside the sandbox fails."""
    def __init__(self, message: str, exit_code: int = 1, stdout: str = "", stderr: str = ""):
        super().__init__(message)
        self.exit_code = exit_code
        self.stdout = stdout
        self.stderr = stderr
